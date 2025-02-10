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
	"github.com/libp2p/go-libp2p/core/metrics"
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
	BOOTSTRAP_PORT     = 6666

	// Just some timeouts for node discovery and stuff
	DiscoveryInterval = 3 * time.Second
	ConnTimeout       = 30 * time.Second

	// Keeping bootstrap address data constant so other nodes can find it - using a single bootstrap node for testing/learning
	BOOTSTRAP_IP = "127.0.0.1" //where bootstrap can be found

	// Proves the nodes identity & allows it to sign messages or state or whatever
	BOOTSTRAP_KEY = "08011240ef34c7af2720bd2aec008490b79ed98ffaf0c7e05911c248e6ef2071ff284f25f019c83ee32540faba9b65162877967487941ff3e5cc968fab0012a193502459"

	// Public identifier derived from the bootstrap key - Other nodes use this to make sure they are connecting to the right bootstrap node
	BOOTSTRAP_ID = "12D3KooWRycpQeKrLnU9k7fLnqG9XH33zdytbXqUjuAN2MwHy2Yp"
)

// Use this instead of globals -- how do i use this lol
type Node struct {
	dht   *dht.IpfsDHT
	topic *pubsub.Topic
	sub   *pubsub.Subscription
	host  host.Host
    metrics metrics.Stats
}

// TODO: This needs to be moved to blockchain.go file
type Transactions struct {
	Lock sync.Mutex
	Data []Transaction
}

// For sending state between peers
// Message types: transaction or blockchain
type NetworkMessage struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// Global variables
var (
	NodeID             = fmt.Sprintf("Node-%d", mrand.Intn(1000)) // giving each Transaction a unique ID to help with tracking - just for testing purposes
	BootstrapMultiaddr = bootstrapAddrSetup(fmt.Sprintf("/ip4/%s/tcp/%d/p2p/%s", BOOTSTRAP_IP, BOOTSTRAP_PORT, BOOTSTRAP_ID))
	Blockchain         []Block
	mutex              = &sync.Mutex{}
	transactions       = &Transactions{
		Data: []Transaction{},
	}
)

func (n *Node) printNodeInfo() {
	for _, addr := range n.host.Addrs() {
		fullAddr := fmt.Sprintf("%s/p2p/%s", addr, n.host.ID())
		log.Printf("[📍] Listening on: %s", fullAddr)
	}
}

// Not using this currently
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

// Setup the bootstrap address
func bootstrapAddrSetup(s string) multiaddr.Multiaddr {
	BootstrapMultiaddr, err := multiaddr.NewMultiaddr(s)
	if err != nil {
		log.Fatal("Failed to create bootstrap multiaddr:", err)
	}

	return BootstrapMultiaddr
}

// Gossip protocol stuff so we can propogate blocks and txs
func setupPubSub(h host.Host) (*pubsub.PubSub, error) {
	return pubsub.NewGossipSub(context.Background(), h)
}

func startDHTDiscovery(ctx context.Context, h host.Host, dht *dht.IpfsDHT) {
	discovery := drouting.NewRoutingDiscovery(dht)
	dutil.Advertise(ctx, discovery, DiscoveryNamespace) // This sets what namespace the DHT will advertise on

	// Run this loop in a goroutine because this is constantly running - if no goroutine it would block the main thread
	go func() {
		for {
			peerChan, err := discovery.FindPeers(ctx, DiscoveryNamespace) // Find peers in the same namespace
			if err != nil {
				log.Printf("Discovery error: %s", err)
				time.Sleep(DiscoveryInterval)
				continue
			}

			// Search through the peer channel for peers
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

func makeHost(ctx context.Context, port int, bootstrapNode bool, node *Node) (host.Host, error) {
	var opts []libp2p.Option

	bw := metrics.NewBandwidthCounter()

	// Base options
	opts = []libp2p.Option{
		libp2p.ListenAddrStrings(fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", port)),
		libp2p.Security(noise.ID, noise.New),
		libp2p.EnableNATService(),   // Enable this so we can hole punch
		libp2p.EnableHolePunching(), // NAT traversal so we can connect using the DHT
		libp2p.BandwidthReporter(bw),
		libp2p.Routing(func(h host.Host) (routing.PeerRouting, error) {
			dht, err := setupDHT(ctx, h, bootstrapNode)
			if err != nil {
				log.Println("Error settin up DHT")
				return nil, err
			}

			node.dht = dht

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

	// Monitor bandwidth
	go func() {
		for {
			stats := bw.GetBandwidthTotals()
            node.metrics = stats
		//	log.Printf("[📊] Node %d - In: %d bytes, Out: %d bytes", port, stats.TotalIn, stats.TotalOut)
			time.Sleep(time.Second * 5)
		}
	}()

	return libp2p.New(opts...)
}

func getBootstrapKey() (crypto.PrivKey, error) {
	privKeyBytes, err := hex.DecodeString(BOOTSTRAP_KEY)
	if err != nil {
		return nil, err
	}
	return crypto.UnmarshalPrivateKey(privKeyBytes) // converts bytes back into usable key
}

// Simulation functions
func simulateTransactions(node *Node) {
	go func() {
		for {
			// Create a tx every 10 seconds
			time.Sleep(10 * time.Second)
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

			if err := node.topic.Publish(context.Background(), mustMarshal(msg)); err != nil {
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

func simulateBlocks(mutex *sync.Mutex, node *Node) {
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

			newBlock.Hash = newBlock.CalculateHash() // Set the hash

			if isBlockValid(newBlock, prevBlock) {
				Blockchain = append(Blockchain, newBlock)

				// Broadcast to network using gossip
				msg := NetworkMessage{
					Type: "blockchain",
					Data: mustMarshal(Blockchain),
				}
				if err := node.topic.Publish(context.Background(), mustMarshal(msg)); err != nil {
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
func commandLineInterface(node *Node) {
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
			fmt.Println("  - api:  Show the REST API port for this node")
			fmt.Println("  - stats:  Show the total io for this node")

		case "chain":
			mutex.Lock()
			bytes, _ := json.MarshalIndent(Blockchain, "", "  ")
			mutex.Unlock()
			fmt.Printf("\n[🔗] Current Blockchain:\n%s\n", string(bytes))

		case "peers":
			fmt.Printf("\n[👥] Connected peers:\n")
			for _, p := range node.host.Network().Peers() {
				fmt.Printf("  - %s\n", p.String()[:12])
			}

		case "tx":
			transactions.Lock.Lock()
			fmt.Println("\n[💼] Current Mempool:")
			for i, tx := range transactions.Data {
				fmt.Printf("[%d] %+v\n", i, tx)
			}
			transactions.Lock.Unlock()

		case "api":
			fmt.Println(apiPort)

		case "stats":
			fmt.Println(apiPort)
            log.Printf("[📊] Node %s - In: %d bytes, Out: %d bytes", node.host.ID().String()[:12], node.metrics.TotalIn, node.metrics.TotalOut)
		default:
			fmt.Println("[❌] Unknown command. Type 'help' for available commands.")
		}
	}
}

func runBootstrapNode(ctx context.Context, port *int, node *Node) {
	log.Printf("[📡] Running as bootstrap node on port %d", *port)
	// If DHT is not nil - set up the routing discovery using the dht
	if node.dht != nil {
		routingDiscovery := drouting.NewRoutingDiscovery(node.dht)
		dutil.Advertise(ctx, routingDiscovery, DiscoveryNamespace)
		log.Printf("[📢] Bootstrap node is advertising on DHT")
	} else {
		log.Println("DHT is nil - reverting to local network discovery")
		network := node.host.Network()
		actualType := reflect.TypeOf(network)
		log.Printf("Actual network type: %v", actualType)
	}
}

func runFullNode(ctx context.Context, node *Node, port *int) {
	log.Printf("[📡] Running as full node on port %d", *port)
	// Connect to bootstrap node
	peerInfo, err := peer.AddrInfoFromP2pAddr(BootstrapMultiaddr)
	if err != nil {
		log.Printf("[⚠️] Failed to parse bootstrap address: %s", err)
	} else {
		if err := node.host.Connect(ctx, *peerInfo); err != nil {
			log.Printf("[⚠️] Failed to connect to bootstrap node: %s", err)
		} else {
			log.Printf("[✅] Connected to bootstrap node")
		}
	}
	if node.dht != nil {
		startDHTDiscovery(ctx, node.host, node.dht)
	} else {
		log.Println("DHT is nil - Error running full node")
	}
}

func joinBlockchainNetworkTopic(ps *pubsub.PubSub) *pubsub.Topic {
	topic, err := ps.Join("blockchain-network")
	if err != nil {
		log.Fatal("[❌] Failed to join topic:", err)
	}
	return topic
}

func messageListener(ctx context.Context, h host.Host, sub *pubsub.Subscription) {
	// Set up message listener
	go func() {
		// Continuously receive messages from peers
		for {
			// Get the next message from the subscription
			msg, err := sub.Next(ctx)
			if err != nil {
				log.Printf("[⚠️] Subscription error: %s", err)
				continue
			}
			if msg.ReceivedFrom == h.ID() {
				continue
			}

			// Unmarshal the incoming message from the peer into a network message struct
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
}

var apiPort int

func mempoolHandler() {
	http.HandleFunc("/tx", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-type", "application/json")
		transactions.Lock.Lock()
		if len(transactions.Data) != 0 {
			json.NewEncoder(w).Encode(transactions.Data)
		}
		transactions.Lock.Unlock()
	})
}

func chainStateHandler() {
	http.HandleFunc("/blockchain", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mutex.Lock()
		if len(Blockchain) != 0 {
			log.Println("Last block in chain:")
			json.NewEncoder(w).Encode(Blockchain[len(Blockchain)-1])
		}
		mutex.Unlock()
	})
}

func apiServer() {
	chainStateHandler()
	mempoolHandler()

	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		log.Println(err)
		return
	}

	addr := listener.Addr().(*net.TCPAddr)
	apiPort := addr.Port

	go func() {
		portStr := fmt.Sprintf(":%d", apiPort)
		log.Printf("API server listening on: %s\n", portStr)
		if err := http.Serve(listener, nil); err != nil {
			log.Fatal("Http server error:", err)
		}
	}()
}


func main() {
	// Single pprof server
	go func() {
		log.Println(http.ListenAndServe("localhost:6069", nil))
	}()

	node := Node{}

	initFakeChain()
	simulateTransactions(&node)
	simulateBlocks(mutex, &node)

	// Create a standard background context
	ctx := context.Background()

	// Parse command line flags
	port := flag.Int("p", 0, "port to listen on")
	isBootstrap := flag.Bool("bootstrap", false, "run as bootstrap node")
	flag.Parse()

	// Gotta provide that dang port number and port flag
	if *port == 0 {
		log.Fatal("[❌] Please provide a port with -p")
	}

	// Make sure user is setting the port number correctly for a bootstrap node
	if *isBootstrap && *port != BOOTSTRAP_PORT {
		log.Fatalf("[❌] Bootstrap node must run on port %d", BOOTSTRAP_PORT)
	}

	// Create libp2p host
	host, err := makeHost(ctx, *port, *isBootstrap, &node)
	if err != nil {
		log.Fatal("[❌] Failed to create host:", err)
	}
	node.host = host

	// Set up pubsub
	ps, err := setupPubSub(node.host)
	if err != nil {
		log.Fatal("[❌] Failed to setup pubsub:", err)
	}

	// Join blockchain topic and subscribe - then we can send and receive messages or state from other peers
	node.topic = joinBlockchainNetworkTopic(ps)
	node.sub, err = node.topic.Subscribe()
	if err != nil {
		log.Fatal("[❌] Failed to subscribe:", err)
	}

	// Set up message listener
	go messageListener(ctx, node.host, node.sub)
	node.printNodeInfo()

	// Bootstrap node setup
	if *isBootstrap {
		runBootstrapNode(ctx, port, &node)
	} else {
		runFullNode(ctx, &node, port)
		apiServer() // run the http server for full nodes only
	}

	// Start CLI
	commandLineInterface(&node)
}
