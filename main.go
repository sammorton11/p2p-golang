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
	"net/http"
	_ "net/http/pprof"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	dutil "github.com/libp2p/go-libp2p/p2p/discovery/util"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	"github.com/multiformats/go-multiaddr"
)

// Constants
const (
	DiscoveryNamespace = "/blockchain/1.0.0"
	BootstrapPort      = 6666
	DiscoveryInterval  = 10 * time.Second
	ConnTimeout        = 30 * time.Second
    BOOTSTRAP_ADDRESS = "/ip4/127.0.0.1/tcp/6666/p2p/12D3KooWRycpQeKrLnU9k7fLnqG9XH33zdytbXqUjuAN2MwHy2Yp"
)

// Blockchain types
type Transactions struct {
	Lock sync.Mutex
	Data []Transaction
}

// Global variables
var (
	BootstrapMultiaddr multiaddr.Multiaddr
	Blockchain         []Block
	mutex              = &sync.Mutex{}
	transactions       = &Transactions{
		Data: []Transaction{},
	}
)

const (
    BOOTSTRAP_KEY = "08011240ef34c7af2720bd2aec008490b79ed98ffaf0c7e05911c248e6ef2071ff284f25f019c83ee32540faba9b65162877967487941ff3e5cc968fab0012a193502459"
    BOOTSTRAP_ID  = "12D3KooWRycpQeKrLnU9k7fLnqG9XH33zdytbXqUjuAN2MwHy2Yp"
)

func init() {
	var err error
	BootstrapMultiaddr, err = multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/127.0.0.1/tcp/6666/p2p/%s", BOOTSTRAP_ID))
	if err != nil {
		log.Fatal("Failed to create bootstrap multiaddr:", err)
	}
}

func setupPubSub(h host.Host) (*pubsub.PubSub, error) {
	return pubsub.NewGossipSub(context.Background(), h)
}

func startDiscovery(ctx context.Context, h host.Host, dht *dht.IpfsDHT) {
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
					stream, err := h.NewStream(ctx, peer.ID, DiscoveryNamespace)
					if err == nil {
						go handleStream(stream)
					}
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
		opts = []dht.Option{dht.Mode(dht.ModeServer)}
	} else {
		opts = []dht.Option{dht.Mode(dht.ModeClient)}
	}

	kdht, err := dht.New(ctx, h, opts...)
	if err != nil {
		return nil, err
	}

	if err = kdht.Bootstrap(ctx); err != nil {
		return nil, err
	}

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
			return setupDHT(ctx, h, bootstrapNode)
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

// Stream handling
func handleStream(s network.Stream) {
	log.Printf("\n[👥] New peer connected: %s\n", s.Conn().RemotePeer().String()[:12])
	rw := bufio.NewReadWriter(bufio.NewReader(s), bufio.NewWriter(s))
	go readData(rw, s.Conn().RemotePeer().String()[:12])
	go writeData(rw, s.Conn().RemotePeer().String()[:12])
}

func readData(rw *bufio.ReadWriter, peerID string) {
	for {
		str, err := rw.ReadString('\n')
		if err != nil {
			log.Printf("[❌] Error reading from peer %s: %v\n", peerID, err)
			return
		}

		if str == "" || str == "\n" {
			continue
		}

		chain := make([]Block, 0)
		if err := json.Unmarshal([]byte(str), &chain); err != nil {
			log.Printf("[❌] Error parsing blockchain from peer %s: %v\n", peerID, err)
			continue
		}

		mutex.Lock()
		if len(chain) > len(Blockchain) {
			log.Printf("\n[📥] Received longer blockchain from peer %s", peerID)
			Blockchain = chain
			bytes, _ := json.MarshalIndent(Blockchain, "", "  ")
			fmt.Printf("\n[🔗] Updated Blockchain:\n%s\n\n> ", string(bytes))
		}
		mutex.Unlock()
	}
}

func writeData(rw *bufio.ReadWriter, peerID string) {
	simulateBlocks(mutex, rw, peerID)

	go func() {
		for {
			time.Sleep(5 * time.Second)
			mutex.Lock()
			bytes, err := json.Marshal(Blockchain)
			if err != nil {
				log.Printf("[❌] Error marshaling blockchain: %v\n", err)
				mutex.Unlock()
				continue
			}

			_, err = rw.WriteString(fmt.Sprintf("%s\n", string(bytes)))
			if err != nil {
				log.Printf("[❌] Error writing to peer %s: %v\n", peerID, err)
				mutex.Unlock()
				continue
			}

			err = rw.Flush()
			if err != nil {
				log.Printf("[❌] Error flushing to peer %s: %v\n", peerID, err)
				mutex.Unlock()
				continue
			}
			mutex.Unlock()
		}
	}()
}

// Simulation functions
func simulateTransactions() {
	go func() {
		for {
			time.Sleep(5 * time.Second)
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
		}
	}()
}

func simulateBlocks(mutex *sync.Mutex, rw *bufio.ReadWriter, peerID string) {
	go func() {
		for {
			time.Sleep(10 * time.Second)
			mutex.Lock()
			prevBlock := Blockchain[len(Blockchain)-1]
			newBlock := NewBlock(
				prevBlock.Index+1,
				prevBlock.Hash,
				[]Transaction{},
				"",
				[]string{},
				[]string{},
				"next miner test",
			)

			if isBlockValid(newBlock, prevBlock) {
				Blockchain = append(Blockchain, newBlock)
			}

			bytes, err := json.Marshal(Blockchain)
			if err != nil {
				log.Printf("[❌] Error marshaling blockchain: %v\n", err)
				mutex.Unlock()
				continue
			}

			_, err = rw.WriteString(fmt.Sprintf("%s\n", string(bytes)))
			if err != nil {
				log.Printf("[❌] Error broadcasting to peer %s: %v\n", peerID, err)
				mutex.Unlock()
				continue
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
	initFakeChain()
	simulateTransactions()

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

	sub, err := topic.Subscribe()
	if err != nil {
		log.Fatal("[❌] Failed to subscribe:", err)
	}

	// Start periodic node availability announcements
	go func() {
		//for {
			msg := fmt.Sprintf("node-available:%s", h.ID())
			if err := topic.Publish(ctx, []byte(msg)); err != nil {
				log.Printf("[⚠️] Failed to publish availability: %s", err)
			}
			time.Sleep(DiscoveryInterval)
		//}
	}()

	// Set up message listener
	go func() {
		for {
			msg, err := sub.Next(ctx)
			if err != nil {
				log.Printf("[⚠️] Subscription error: %s", err)
				continue
			}
			if msg.ReceivedFrom == h.ID() {
				continue // Skip own messages
			}
			log.Printf("[📨] Received: %s from %s", string(msg.Data), msg.ReceivedFrom.String()[:12])
		}
	}()

	// Set protocol handler
	h.SetStreamHandler(DiscoveryNamespace, handleStream)

	// Print node information
	for _, addr := range h.Addrs() {
		fullAddr := fmt.Sprintf("%s/p2p/%s", addr, h.ID())
		log.Printf("[📍] Listening on: %s", fullAddr)
	}

	// Bootstrap node setup
	if *isBootstrap {
		log.Printf("[📡] Running as bootstrap node on port %d", *port)
		// Get the DHT from the host's network
		if dht, ok := h.Network().(interface{ GetRoutingBackend() *dht.IpfsDHT }); ok {
			routingDiscovery := drouting.NewRoutingDiscovery(dht.GetRoutingBackend())
			dutil.Advertise(ctx, routingDiscovery, DiscoveryNamespace)
			log.Printf("[📢] Bootstrap node is advertising on DHT")
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

		// Start peer discovery
		if dht, ok := h.Network().(interface{ GetRoutingBackend() *dht.IpfsDHT }); ok {
			startDiscovery(ctx, h, dht.GetRoutingBackend())
		}
	}

	// Start CLI
	commandLineInterface(h)
}

// Ignore this
func generateNewBootstrapKeys() {
    priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, -1)
    if err != nil {
        log.Fatal(err)
    }
    privBytes, err := crypto.MarshalPrivateKey(priv)
    if err != nil {
        log.Fatal(err)
    }
    
    // Print the key info
    log.Printf("Private key: %s", hex.EncodeToString(privBytes))
    id, _ := peer.IDFromPrivateKey(priv)
    log.Printf("Peer ID: %s", id)
}
