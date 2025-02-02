package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	mrand "math/rand"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	dutil "github.com/libp2p/go-libp2p/p2p/discovery/util"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	"github.com/multiformats/go-multiaddr"
)

/*
TODO:
    - Bootstrap IP will come from hosting service in prod
    - Can use ngrok to sub this for testing? i guess? idk.
    - Replace BOOTSTRAP IP with your machines IP for two machines to connect
*/

// Constants
const (
	DiscoveryNamespace = "/blockchain/1.0.0"
	BootstrapPort      = 6666
	DiscoveryInterval  = 3 * time.Second
	ConnTimeout        = 30 * time.Second
	BOOTSTRAP_IP       = "127.0.0.1"
)

// Blockchain types
type Transactions struct {
	Lock sync.Mutex
	Data []Transaction
}

type NetworkMessage struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// Global variables
var (
	NodeID             = fmt.Sprintf("Node-%d", mrand.Intn(1000)) // giving each Transaction a unique ID to help with tracking - just for testing purposes
	BootstrapMultiaddr multiaddr.Multiaddr
	Blockchain         []Block
	mutex              = &sync.Mutex{}
	transactions       = &Transactions{
		Data: []Transaction{},
	}
	globalDHT   *dht.IpfsDHT
	globalTopic *pubsub.Topic
	globalSub   *pubsub.Subscription
)

const (
	BOOTSTRAP_KEY = "08011240ef34c7af2720bd2aec008490b79ed98ffaf0c7e05911c248e6ef2071ff284f25f019c83ee32540faba9b65162877967487941ff3e5cc968fab0012a193502459"
	BOOTSTRAP_ID  = "12D3KooWRycpQeKrLnU9k7fLnqG9XH33zdytbXqUjuAN2MwHy2Yp"
)

// Get IP address from machine for Bootstrap node
func getIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		log.Println(err.Error())
	}

	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	return "127.0.0.1"
}

func init() {
	var err error
	//BootstrapMultiaddr, err = multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/127.0.0.1/tcp/6666/p2p/%s", BOOTSTRAP_ID))
	ip := BOOTSTRAP_IP
	log.Println(ip)

	BootstrapMultiaddr, err = multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/%s/tcp/6666/p2p/%s", ip, BOOTSTRAP_ID))
	if err != nil {
		log.Fatal("Failed to create bootstrap multiaddr:", err)
	}
}

func setupPubSub(h host.Host) (*pubsub.PubSub, error) {
	return pubsub.NewGossipSub(context.Background(), h)
}

func startDiscovery(ctx context.Context, h host.Host, dht *dht.IpfsDHT) {
	log.Println("\nSTARTING DISCOVERY")
	discovery := drouting.NewRoutingDiscovery(dht)
	dutil.Advertise(ctx, discovery, DiscoveryNamespace)

	go func() {
		for {
			peerChan, err := discovery.FindPeers(ctx, DiscoveryNamespace)
			if err != nil {
				log.Printf("Discovery error: %s", err)
				time.Sleep(DiscoveryInterval)
				continue
			}

			for peer := range peerChan {
				if peer.ID == h.ID() || len(h.Network().ConnsToPeer(peer.ID)) > 0 {
					continue
				}

				connectCtx, cancel := context.WithTimeout(ctx, ConnTimeout)
				if err := h.Connect(connectCtx, peer); err != nil {
					log.Printf("Connection failed: %s", err)
				} else {
					log.Printf("Connected to peer: %s", peer.ID.String()[:12])
				}
				cancel()
			}

			time.Sleep(DiscoveryInterval)
		}
	}()
}

func setupDHT(ctx context.Context, h host.Host, bootstrapNode bool) (*dht.IpfsDHT, error) {
	var opts []dht.Option

	if bootstrapNode {
		log.Printf("[DHT] Initializing bootstrap node in server mode")
		opts = []dht.Option{
			dht.Mode(dht.ModeServer),
			dht.ProtocolPrefix("/blockchain"),
		}
	} else {
		log.Printf("[DHT] Initializing peer node in client mode")
		opts = []dht.Option{
			dht.Mode(dht.ModeAutoServer),
			dht.ProtocolPrefix("/blockchain"),
			dht.BootstrapPeers(peer.AddrInfo{
				ID:    peer.ID(BOOTSTRAP_ID),
				Addrs: []multiaddr.Multiaddr{BootstrapMultiaddr},
			}),
		}
	}

	kdht, err := dht.New(ctx, h, opts...)
	if err != nil {
		return nil, err
	}

	if err = kdht.Bootstrap(ctx); err != nil {
		return nil, err
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second * 30):
				if err := kdht.Bootstrap(ctx); err != nil {
					log.Printf("[DHT] Failed to refresh routing table: %s", err)
				}
			}
		}
	}()

	return kdht, nil
}

func makeHost(ctx context.Context, port int, bootstrapNode bool) (host.Host, error) {
	var opts []libp2p.Option

	// Base options
	opts = []libp2p.Option{
		libp2p.ListenAddrStrings(fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", port)),
		libp2p.Security(noise.ID, noise.New),
		libp2p.EnableNATService(),
		libp2p.EnableHolePunching(),
		libp2p.Routing(func(h host.Host) (routing.PeerRouting, error) {
			dht, err := setupDHT(ctx, h, bootstrapNode)
			if err != nil {
				log.Println("Error settin up DHT")
				return nil, err
			}

			// Should we just add the addresses to the dht here.. or.. idk..
			globalDHT = dht

			return dht, nil
		}),
	}

	// Add bootstrap node specific options
	if bootstrapNode {
		privKey, err := getBootstrapKey()
		if err != nil {
			return nil, fmt.Errorf("bootstrap key error: %w", err)
		}
		opts = append(opts, libp2p.Identity(privKey))
	}

	return libp2p.New(opts...)
}

func getBootstrapKey() (crypto.PrivKey, error) {
	privKeyBytes, err := hex.DecodeString(BOOTSTRAP_KEY)
	if err != nil {
		return nil, err
	}
	return crypto.UnmarshalPrivateKey(privKeyBytes)
}

// Simulation functions
func simulateTransactions() {
	go func() {
		for {
			// Create a tx every 30 seconds
			time.Sleep(30 * time.Second)
			tx := Transaction{
				Sender:        mrand.Intn(10000),
				Recipient:     fmt.Sprintf("Recipient %d", mrand.Intn(10000)),
				Signature:     fmt.Sprintf("Signature %d", mrand.Intn(10000)),
				Amount:        mrand.Intn(1000) + 1,
				Properties:    fmt.Sprintf("Property %d", mrand.Intn(10)),
				Computational: fmt.Sprintf("Data %d", mrand.Intn(10)),
				Nonce:         mrand.Intn(10000),
			}

			transactions.Lock.Lock()
			transactions.Data = append(transactions.Data, tx)
			transactions.Lock.Unlock()

			msg := NetworkMessage{
				Type: "transaction",
				Data: mustMarshal(tx),
			}

			if err := globalTopic.Publish(context.Background(), mustMarshal(msg)); err != nil {
				log.Printf("Failed to publish tx: %v", err)
			}
		}
	}()
}

func mustMarshal(v interface{}) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}

	return b
}

func simulateBlocks(mutex *sync.Mutex) {
	go func() {
		for {
			time.Sleep(10 * time.Second)
			mutex.Lock()
			prevBlock := Blockchain[len(Blockchain)-1]

			// Get pending transactions
			transactions.Lock.Lock()
			pendingTx := transactions.Data
			//transactions.Data = []Transaction{} // Clear the pool
			transactions.Lock.Unlock()

			newBlock := NewBlock(
				prevBlock.Index+1,
				prevBlock.Hash,
				pendingTx, // Add pending transactions
				"",
				[]string{},
				[]string{},
				fmt.Sprintf("miner-%s", NodeID),
			)

			newBlock.Hash = newBlock.CalculateHash() // Set the hash!

			if isBlockValid(newBlock, prevBlock) {
				Blockchain = append(Blockchain, newBlock)

				// Broadcast via PubSub
				msg := NetworkMessage{
					Type: "blockchain",
					Data: mustMarshal(Blockchain),
				}
				if err := globalTopic.Publish(context.Background(), mustMarshal(msg)); err != nil {
					log.Printf("Failed to publish chain: %v", err)
				}
			}
			mutex.Unlock()
		}
	}()
}

func isBlockValid(newBlock, oldBlock Block) bool {
	if oldBlock.Index+1 != newBlock.Index {
		log.Printf("[❌] Invalid block index")
		return false
	}
	if oldBlock.Hash != newBlock.PreviousHash {
		log.Printf("[❌] Invalid previous hash")
		return false
	}
	if newBlock.CalculateHash() != newBlock.Hash {
		log.Printf("[❌] Invalid hash")
		return false
	}
	return true
}

func initFakeChain() {
	genesisBlock := NewBlock(
		0,
		"genesis",
		[]Transaction{},
		"",
		[]string{},
		[]string{},
		"genesis",
	)
	Blockchain = append(Blockchain, genesisBlock)
}

// CLI Interface
func commandLineInterface(h host.Host) {
	stdReader := bufio.NewReader(os.Stdin)

	for {
		fmt.Print("> ")
		sendData, err := stdReader.ReadString('\n')
		if err != nil {
			log.Fatal(err)
		}

		cmd := strings.TrimSpace(sendData)
		switch cmd {
		case "help":
			fmt.Println("\n[📖] Commands:")
			fmt.Println("  - chain: Show current blockchain")
			fmt.Println("  - peers: Show connected peers")
			fmt.Println("  - tx:    Show pending transactions")
			fmt.Println("  - help:  Show this message")
		case "chain":
			mutex.Lock()
			bytes, _ := json.MarshalIndent(Blockchain, "", "  ")
			mutex.Unlock()
			fmt.Printf("\n[🔗] Current Blockchain:\n%s\n", string(bytes))
		case "peers":
			fmt.Printf("\n[👥] Connected peers:\n")
			for _, p := range h.Network().Peers() {
				fmt.Printf("  - %s\n", p.String()[:12])
			}
		case "tx":
			transactions.Lock.Lock()
			fmt.Println("\n[💼] Current Mempool:")
			for i, tx := range transactions.Data {
				fmt.Printf("[%d] %+v\n", i, tx)
			}
			transactions.Lock.Unlock()
		default:
			fmt.Println("[❌] Unknown command. Type 'help' for available commands.")
		}
	}
}

func main() {
	initFakeChain()
	simulateTransactions()
	simulateBlocks(mutex)

	// Enable pprof for debugging
	go func() {
		log.Println(http.ListenAndServe("localhost:6061", nil))
	}()

	// Create a background context
	ctx := context.Background()

	// Parse command line flags
	port := flag.Int("p", 0, "port to listen on")
	isBootstrap := flag.Bool("bootstrap", false, "run as bootstrap node")
	flag.Parse()

	if *port == 0 {
		log.Fatal("[❌] Please provide a port with -p")
	}

	// Validate bootstrap port
	if *isBootstrap && *port != BootstrapPort {
		log.Fatalf("[❌] Bootstrap node must run on port %d", BootstrapPort)
	}

	// Initialize blockchain and start transaction simulator

	// Create libp2p host
	h, err := makeHost(ctx, *port, *isBootstrap)
	if err != nil {
		log.Fatal("[❌] Failed to create host:", err)
	}

	// Set up pubsub
	ps, err := setupPubSub(h)
	if err != nil {
		log.Fatal("[❌] Failed to setup pubsub:", err)
	}

	// Join blockchain topic and subscribe
	topic, err := ps.Join("blockchain-network")
	if err != nil {
		log.Fatal("[❌] Failed to join topic:", err)
	}

	globalTopic = topic
	sub, err := topic.Subscribe()
	if err != nil {
		log.Fatal("[❌] Failed to subscribe:", err)
	}
	globalSub = sub

	// Set up message listener
	go func() {
		for {
			msg, err := globalSub.Next(ctx)
			if err != nil {
				log.Printf("[⚠️] Subscription error: %s", err)
				continue
			}
			if msg.ReceivedFrom == h.ID() {
				continue
			}

			var netMsg NetworkMessage
			if err := json.Unmarshal(msg.Data, &netMsg); err != nil {
				continue
			}

			switch netMsg.Type {
			case "transaction":
				var tx Transaction
				if err := json.Unmarshal(netMsg.Data, &tx); err != nil {
					continue
				}
				transactions.Lock.Lock()
				transactions.Data = append(transactions.Data, tx)
				transactions.Lock.Unlock()

			case "blockchain":
				var chain []Block
				if err := json.Unmarshal(netMsg.Data, &chain); err != nil {
					continue
				}
				mutex.Lock()
				if len(chain) > len(Blockchain) {
					Blockchain = chain
				}
				mutex.Unlock()
			}
		}
	}()

	// Print node information
	for _, addr := range h.Addrs() {
		fullAddr := fmt.Sprintf("%s/p2p/%s", addr, h.ID())
		log.Printf("[📍] Listening on: %s", fullAddr)
	}

	// Bootstrap node setup
	if *isBootstrap {
		log.Printf("[📡] Running as bootstrap node on port %d", *port)
		// If DHT is not nil - set up the routing discovery using the dht
		if globalDHT != nil {
			routingDiscovery := drouting.NewRoutingDiscovery(globalDHT)
			dutil.Advertise(ctx, routingDiscovery, DiscoveryNamespace)
			log.Printf("[📢] Bootstrap node is advertising on DHT")
		} else {
			network := h.Network()
			actualType := reflect.TypeOf(network)
			log.Printf("Actual network type: %v", actualType)
		}

	} else {
		log.Printf("[📡] Running as full node on port %d", *port)

		// Connect to bootstrap node
		peerInfo, err := peer.AddrInfoFromP2pAddr(BootstrapMultiaddr)
		if err != nil {
			log.Printf("[⚠️] Failed to parse bootstrap address: %s", err)
		} else {
			if err := h.Connect(ctx, *peerInfo); err != nil {
				log.Printf("[⚠️] Failed to connect to bootstrap node: %s", err)
			} else {
				log.Printf("[✅] Connected to bootstrap node")
			}
		}

		if globalDHT != nil {
			startDiscovery(ctx, h, globalDHT)
		} else {
			log.Println("")
		}
	}

	// Start CLI
	commandLineInterface(h)
}
