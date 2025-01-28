package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	mrand "math/rand"
	"net/http"
	_ "net/http/pprof"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	"github.com/multiformats/go-multiaddr"
)

var Blockchain []Block
var mutex = &sync.Mutex{} // locking for modifying the blockchain one at a time

func makeBasicHost(listenPort int, randseed int64) (host.Host, error) {
	var r io.Reader
	if randseed == 0 { // this is for the unique id's for the peers
		r = rand.Reader
	} else {
		r = mrand.New(mrand.NewSource(randseed))
	}

	// Creating an ID for the node
	priv, _, err := crypto.GenerateKeyPairWithReader(crypto.RSA, 2048, r)
	if err != nil {
		return nil, err
	}

	// Networking set up
	// The Nodes "mailbox"
	opts := []libp2p.Option{
		libp2p.ListenAddrStrings(fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", listenPort)),
		libp2p.Identity(priv),
		libp2p.Security(noise.ID, noise.New),
	}

	// Setting up the basic host with configs
	basicHost, err := libp2p.New(opts...)
	if err != nil {
		return nil, err
	}

	// Creating the host address - this is JUST the peer identity part
	hostAddr, _ := multiaddr.NewMultiaddr(fmt.Sprintf("/p2p/%s", basicHost.ID()))
	addr := basicHost.Addrs()[0]           // only get first available address for this prototype
	fullAddr := addr.Encapsulate(hostAddr) // build the full host address

	log.Printf("\n[🔗] Your peer address: %s\n", fullAddr)
	log.Printf("[📡] Your peer ID: %s\n", basicHost.ID().String()[:12])

	return basicHost, nil
}

// This handles the call from other peers
func handleStream(s network.Stream) {
	log.Printf("\n[👥] New peer connected: %s\n", s.Conn().RemotePeer().String()[:12])

	// this is for sending and receiving data from self and peers - Read to stream; Write to stream
	rw := bufio.NewReadWriter(bufio.NewReader(s), bufio.NewWriter(s))

	// concurrency so peers can communicate at the same time
	// Taking the first 12 chars instead of full ID
	go readData(rw, s.Conn().RemotePeer().String()[:12])
	go writeData(rw, s.Conn().RemotePeer().String()[:12])
}

// Reading the data from the "client" and then updating its own chain copy
func readData(rw *bufio.ReadWriter, peerID string) {
	for {
		str, err := rw.ReadString('\n') // read new data until new line
		if err != nil {
			log.Printf("[❌] Error reading from peer %s: %v\n", peerID, err)
			return
		}

		if str == "" {
			continue
		}

		if str != "\n" {
			chain := make([]Block, 0)
			if err := json.Unmarshal([]byte(str), &chain); err != nil {
				log.Printf("[❌] Error parsing blockchain from peer %s: %v\n", peerID, err)
				continue
			}

			mutex.Lock() // lock so only one goroutine has access
			if len(chain) > len(Blockchain) {
				log.Printf("\n[📥] Received longer blockchain from peer %s", peerID)
				Blockchain = chain
				bytes, _ := json.MarshalIndent(Blockchain, "", "  ")
				fmt.Printf("\n[🔗] Updated Blockchain:\n%s\n\n> ", string(bytes))
			}
			mutex.Unlock() // unlock so other goroutines can now access
		}
	}
}

/*
Automatic broadcasting:
  - Every 5 seconds
  - Takes the blockchain
  - Converts it to JSON
  - Sends it to all peers
  - This keeps everyone in sync
*/
func writeData(rw *bufio.ReadWriter, peerID string) {
	// Periodically write valid blocks -- simulating adding valid blocks from a mempool
	simulateBlocks(mutex, rw, peerID)
	handled := true

	// Periodic blockchain broadcast
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

    validCmd := map[string]bool {
        "help": true,
        "add": true,
        "tx": true,
        "chain": true,
        "peers": true,
    }


	// Read user input
	stdReader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("> ")
		sendData, err := stdReader.ReadString('\n')
		if err != nil {
			log.Printf("[❌] Error reading from stdin: %v\n", err)
			return
		}

		sendData = strings.TrimSpace(sendData) // remove whitespace
        if !validCmd[sendData] {
            fmt.Printf("\n[❌] Unknown command: %s\n", sendData)
			fmt.Println("Please enter a valid command - Type 'help' to see a list of commands")
            continue
        }

		switch sendData {
		case "help":
			fmt.Println("\n[📖] Commands:")
			fmt.Println("  - add   [📦]: Manually creates a new block")
			fmt.Println("  - tx    [📥]: Display incoming transactions")
			fmt.Println("  - chain [🔗]: Shows current blockchain")
			fmt.Println("  - peers [👥]: Shows connected peers")
			fmt.Println("  - help  [📖]: Shows this help message")
			continue

		case "chain":
			mutex.Lock()
			bytes, _ := json.MarshalIndent(Blockchain, "", "  ")
			mutex.Unlock()
			fmt.Printf("\n[🔗] Current Blockchain:\n%s\n", string(bytes))
			continue

		case "peers":
			fmt.Printf("\n[👥] Connected to peer: %s\n", peerID)
			continue

		case "tx":
			transactions.Lock.Lock()
			fmt.Println("\n[] Current Mempool:")
			for i, tx := range transactions.Data {
				fmt.Printf("[%d] %+v\n", i, tx)
				fmt.Printf("[%d] Properties %+v\n", i, tx.Properties)
			}
			transactions.Lock.Unlock()
			continue

		case "add":
			mutex.Lock()
			prevBlock := Blockchain[len(Blockchain)-1]
			newBlock := generateBlock(prevBlock.Index+1, prevBlock.Hash)
			if isBlockValid(newBlock, prevBlock) {
				Blockchain = append(Blockchain, newBlock)
				log.Printf("\n[✨] Created new block: %d\n", newBlock.Index)
				spew.Printf("[📦] Block details:\n%+v\n\n", newBlock)
                mutex.Unlock()
                continue
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
			continue
		}

		// Flush forces the data to actually be sent.
		// Without it data might sit in a buffer waiting to be sent.
		if handled {
			err = rw.Flush()
		}
		if err != nil {
			log.Printf("[❌] Error flushing to peer %s: %v\n", peerID, err)
			mutex.Unlock()
			continue
		}
		mutex.Unlock()
	}
}

func main() {
	// For profiling
	go func() {
		log.Println(http.ListenAndServe("localhost:6061", nil))
	}()
	// Simulate transaction mempool
	simulateTransactions()

	genesisBlock := NewBlock(
		0,               // index
		"genesis",       // previous hash
		[]Transaction{}, // empty transactions
		"genesis",       // signature
		[]string{},      // merkle root
		[]string{},      // new keys
		"genesis",       // next miner
	)
	genesisBlock.Hash = genesisBlock.CalculateHash()
	Blockchain = append(Blockchain, genesisBlock)

	// command line args to set the port, what peer to connect to, or random seed
	listenF := flag.Int("l", 0, "wait for incoming connections")
	target := flag.String("d", "", "target peer to dial")
	seed := flag.Int64("seed", 0, "set random seed for id generation")
	flag.Parse()

	if *listenF == 0 {
		log.Fatal("[❌] Please provide a port to bind on with -l")
	}

	// Create host application
	ha, err := makeBasicHost(*listenF, *seed)
	if err != nil {
		log.Fatal(err)
	}

	// If no target - be a bootstrap node instead
	// Else - attempt to connect to the existing node
	if *target == "" {
		log.Println("\n[👂] Listening for connections...")
		ha.SetStreamHandler("/p2p/1.0.0", handleStream)
		select {}
	} else {
		ha.SetStreamHandler("/p2p/1.0.0", handleStream)
		// Get address of the peer
		peerAddr, err := multiaddr.NewMultiaddr(*target)
		if err != nil {
			log.Fatal(err)
		}

		// Peer info for connecting
		peerinfo, err := peer.AddrInfoFromP2pAddr(peerAddr)
		if err != nil {
			log.Fatal(err)
		}

		// Attempt to connect to peer
		log.Printf("\n[🔄] Connecting to peer: %s\n", *target)
		if err := ha.Connect(context.Background(), *peerinfo); err != nil {
			log.Fatal(err)
		}
		// Taking in the stream from the peer
		log.Printf("[✅] Connected to peer: %s\n", peerinfo.ID.String()[:12])
		stream, err := ha.NewStream(context.Background(), peerinfo.ID, "/p2p/1.0.0")
		if err != nil {
			log.Fatal(err)
		}

		// Set up the two way communication
		// Create buffered reader and writer for net comm
		// Start goroutines to handle sending and receiving data
		rw := bufio.NewReadWriter(bufio.NewReader(stream), bufio.NewWriter(stream))
		go writeData(rw, peerinfo.ID.String()[:12])
		go readData(rw, peerinfo.ID.String()[:12])

		// wait forever while goroutines do all of the work
		select {}
	}
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
	if calculateHash(newBlock) != newBlock.Hash {
		log.Printf("[❌] Invalid hash")
		return false
	}
	return true
}

func calculateHash(block Block) string {
	record := fmt.Sprintf("%d%d%v%s%s%v%v%s",
		block.Index,
		block.Timestamp,
		block.Transactions,
		block.PreviousHash,
		block.Signature,
		block.MerkleRoot,
		block.NewKeys,
		block.NextMiner,
	)
	h := sha256.New()
	h.Write([]byte(record))
	hashed := h.Sum(nil)
	return hex.EncodeToString(hashed)
}

func generateBlock(index int, hash string) Block {
	/*     oldBlockHash := calculateHash(oldBlock) */
	newBlock := NewBlock(
		index,             // index
		hash,              // previous hash
		[]Transaction{},   // empty transactions
		"",                // signature
		[]string{},        // merkle root
		[]string{},        // new keys
		"next miner test", // next miner
	)

	newBlock.Hash = calculateHash(newBlock)

	return newBlock
}

// Simulate a mempool of Transactions coming through
type Transactions struct {
	Lock sync.Mutex
	Data []Transaction
}

var transactions = &Transactions{
	Data: []Transaction{},
}

// Simulate transaction stream
func simulateTransactions() {
	go func() {
		for {
			time.Sleep(5 * time.Second) // Simulate a transaction every 5 seconds
			tx := Transaction{
				Sender:        mrand.Intn(10000),
				Recipient:     fmt.Sprintf("Recipient %d", mrand.Intn(10000)),
				Signature:     fmt.Sprintf("Signature %d", mrand.Intn(10000)),
				Amount:        mrand.Intn(1000) + 1,
				Properties:    fmt.Sprintf("Property %d", mrand.Intn(10)),
				Computational: fmt.Sprintf("Data %d", mrand.Intn(10)),
				Nonce:         mrand.Intn(10000),
			}

			// Add transaction to the mempool
			transactions.Lock.Lock()
			transactions.Data = append(transactions.Data, tx)
			transactions.Lock.Unlock()

			//log.Printf("[📥] New simulated transaction: %+v\n", tx)
		}
	}()
}

func simulateBlocks(mutex *sync.Mutex, rw *bufio.ReadWriter, peerID string) {
	go func() {
		for {
			time.Sleep(10 * time.Second)
			mutex.Lock()
			prevBlock := Blockchain[len(Blockchain)-1]
			newBlock := generateBlock(prevBlock.Index+1, prevBlock.Hash)

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
