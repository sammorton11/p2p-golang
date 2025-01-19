package main

import (
	"bufio"
	"context"
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
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/routing"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	dutil "github.com/libp2p/go-libp2p/p2p/discovery/util"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	"github.com/multiformats/go-multiaddr"
)

type Transactions struct {
	Lock sync.Mutex
	Data []Transaction
}

var (
	Blockchain   []Block
	mutex        = &sync.Mutex{}
	transactions = &Transactions{
		Data: []Transaction{},
	}
)

const BOOTSTRAP_PORT = 6666

func getBootstrapPeerAddr() multiaddr.Multiaddr {
	addr, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", BOOTSTRAP_PORT))
	if err != nil {
		log.Fatal(err)
	}
	return addr
}

// Add at the top of your file with other vars
var defaultBootstrapPeers = []multiaddr.Multiaddr{
	// We'll fill this in the main function when bootstrap node starts
	// This will be our known bootstrap node address
}

func setupPubSub(h host.Host) (*pubsub.PubSub, error) {
	return pubsub.NewGossipSub(context.Background(), h)
}

func discoverPeers(ctx context.Context, h host.Host, dht *dht.IpfsDHT) {
	discovery := drouting.NewRoutingDiscovery(dht)

	// Advertise our service
	dutil.Advertise(ctx, discovery, "/blockchain/1.0.0")

	go func() {
		for {
			// Standard FindPeers method
			peerChan, err := discovery.FindPeers(ctx, "/blockchain/1.0.0")
			if err != nil {
				log.Printf("Peer discovery error: %v", err)
				time.Sleep(time.Minute)
				continue
			}

			for peer := range peerChan {
				// Skip self and already connected peers
				if peer.ID == h.ID() ||
					len(h.Network().ConnsToPeer(peer.ID)) > 0 {
					continue
				}

				// Attempt connection with timeout
				connectCtx, cancel := context.WithTimeout(ctx, time.Minute)
				err := h.Connect(connectCtx, peer)
				cancel()

				if err != nil {
					log.Printf("Failed to connect to peer %s: %v",
						peer.ID.String()[:12], err)
					continue
				}

				log.Printf("Connected to new peer: %s", peer.ID.String()[:12])

				// Attempt to open stream
				stream, err := h.NewStream(ctx, peer.ID, "/blockchain/1.0.0")
				if err != nil {
					log.Printf("Stream error: %v", err)
					continue
				}

				rw := bufio.NewReadWriter(bufio.NewReader(stream), bufio.NewWriter(stream))
				go readData(rw, peer.ID.String()[:12])
				go writeData(rw, peer.ID.String()[:12])
			}

			// Wait between discovery cycles
			time.Sleep(time.Minute)
		}
	}()
}

func setupDHT(ctx context.Context, host host.Host, bootstrapPeer bool) (*dht.IpfsDHT, error) {
	var dhtOpts []dht.Option
	if bootstrapPeer {
		fmt.Println("This is a bootstrap")
		// Bootstrap nodes run in server mode
		log.Printf("[🔧] Creating DHT in server mode (bootstrap node)")
		dhtOpts = []dht.Option{dht.Mode(dht.ModeServer)}
	} else {
		// Regular nodes run in client mode
		log.Printf("[🔧] Creating DHT in client mode (full node)")
		dhtOpts = []dht.Option{dht.Mode(dht.ModeClient)}
	}

	kdht, err := dht.New(ctx, host, dhtOpts...)
	if err != nil {
		return nil, err
	}

	// Bootstrap the DHT
	if err = kdht.Bootstrap(ctx); err != nil {
		return nil, fmt.Errorf("failed to bootstrap DHT: %s", err)
	}

	return kdht, nil
}

func makeHost(ctx context.Context, port int, bootstrapPeer bool) (host.Host, *dht.IpfsDHT, error) {
	// Create DHT instance first
	var kadDHT *dht.IpfsDHT

	// Create routing function that will set up DHT
	routing := func(h host.Host) (routing.PeerRouting, error) {
		var err error
		kadDHT, err = setupDHT(ctx, h, bootstrapPeer)
		return kadDHT, err
	}

	// Create libp2p options including routing
	opts := []libp2p.Option{
		libp2p.ListenAddrStrings(fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", port)),
		libp2p.Security(noise.ID, noise.New),
		libp2p.Routing(routing),
		libp2p.EnableNATService(),
	}

	h, err := libp2p.New(opts...)
	if err != nil {
		return nil, nil, err
	}

	// Print host info
	for _, addr := range h.Addrs() {
		fullAddr := fmt.Sprintf("%s/p2p/%s", addr, h.ID())
		log.Printf("[📍] Listening on: %s", fullAddr)
	}

	return h, kadDHT, nil
}

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
		"genesis",
		[]string{},
		[]string{},
		"genesis",
	)
	Blockchain = append(Blockchain, genesisBlock)
}

func main() {
	// For profiling
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

	// Initialize blockchain and start transaction simulator
	initFakeChain()
	simulateTransactions()

	// Create libp2p host with DHT
	h, kadDHT, err := makeHost(ctx, *port, *isBootstrap)
	if err != nil {
		log.Fatal(err)
	}

	// Setup PubSub
	ps, err := setupPubSub(h)
	if err != nil {
		log.Fatal(err)
	}

	// Join blockchain topic
	topic, err := ps.Join("blockchain-network")
	if err != nil {
		log.Fatal(err)
	}

	// Subscribe to receive messages
	sub, err := topic.Subscribe()
	if err != nil {
		log.Fatal(err)
	}

	// Publish node availability
	// Publish node availability
	go func() {
		for {
			topic.Publish(context.Background(), []byte(fmt.Sprintf("node-available:%s", h.ID())))
			time.Sleep(time.Minute)
		}
	}()

	// Listen for incoming messages
	go func() {
		for {
			msg, err := sub.Next(context.Background())
			if err != nil {
				log.Println("Subscription error:", err)
				continue
			}
			log.Printf("Received pubsub message: %s", string(msg.Data))
		}
	}()

	// Set protocol handler for all nodes
	h.SetStreamHandler("/blockchain/1.0.0", handleStream)

	// Bootstrap the DHT
	if err := kadDHT.Bootstrap(ctx); err != nil {
		log.Printf("[⚠️] Failed to bootstrap DHT: %s", err)
	}

	// Print the full address
	hostAddr, _ := multiaddr.NewMultiaddr(fmt.Sprintf("/p2p/%s", h.ID()))
	addr := h.Addrs()[0]
	fullAddr := addr.Encapsulate(hostAddr)
	log.Printf("\n[📝] Full node address: %s\n", fullAddr)

	// If this is the bootstrap node, update the defaultBootstrapPeers
	if *isBootstrap {
		if *port != BOOTSTRAP_PORT {
			log.Fatal("Bootstrap node must run on port", BOOTSTRAP_PORT)
		}
		log.Printf("[📡] Running as bootstrap node on port %d\n", *port)

		routingDiscovery := drouting.NewRoutingDiscovery(kadDHT)
		dutil.Advertise(ctx, routingDiscovery, "/blockchain/1.0.0")
		log.Printf("[📢] Bootstrap node is advertising on DHT")
	} else {
		log.Printf("[📡] Running as full node on port %d\n", *port)
		// Connect to known bootstrap node address
		bootstrapAddr := getBootstrapPeerAddr()
		defaultBootstrapPeers = []multiaddr.Multiaddr{bootstrapAddr}
		discoverPeers(ctx, h, kadDHT)
	}

	// Command line interface
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
