package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"
)

// Transaction represents a single transaction in the blockchain
type Transaction struct {
	Sender        int    `json:"sender"`
	Recipient     string `json:"recipient"`
	Signature     string `json:"signature"`
	Amount        int    `json:"amount"`
	Properties    string `json:"properties"`
	Computational string `json:"computational"`
	Nonce         int    `json:"nonce"`
}

// Block represents a block in the blockchain
type Block struct {
	Index        int           `json:"index"`
	PreviousHash string        `json:"previous_hash"`
	Hash         string        `json:"hash"`
	Timestamp    int64         `json:"timestamp"`
	Transactions []Transaction `json:"transactions"`
	Signature    string        `json:"signature"`
	MerkleRoot   []string      `json:"merkle_root"`
	NewKeys      []string      `json:"new_keys"`
	NextMiner    string        `json:"next_miner"`
}

// Methods for Transaction
func NewTransaction(sender int, recipient string, amount int, computational string, properties string, nonce int) *Transaction {
	return &Transaction{
		Sender:        sender,
		Recipient:     recipient,
		Amount:        amount,
		Properties:    properties,
		Computational: computational,
		Nonce:         nonce,
	}
}

func (t *Transaction) GetTransactionDetails() map[string]string {
	return map[string]string{
		"sender":        strconv.Itoa(t.Sender),
		"recipient":     t.Recipient,
		"signature":     t.Signature,
		"amount":        strconv.Itoa(t.Amount),
		"properties":    t.Properties,
		"computational": t.Computational,
		"nonce":         strconv.Itoa(t.Nonce),
	}
}

func (t *Transaction) SetSignature(signature string) {
	t.Signature = signature
}

// Methods for Block
func NewBlock(index int, previousHash string, transactions []Transaction,
	signature string, merkleRoot []string, newKeys []string,
	nextMiner string) Block {
	return Block{
		Index:        index,
		PreviousHash: previousHash,
		Timestamp:    time.Now().Unix(),
		Transactions: transactions,
		Signature:    signature,
		MerkleRoot:   merkleRoot,
		NewKeys:      newKeys,
		NextMiner:    nextMiner,
	}
}

func (b *Block) CalculateHash() string {
	record := fmt.Sprintf("%d%s%d%v%s%v%v%s",
		b.Index,
		b.PreviousHash,
		b.Timestamp,
		b.Transactions,
		b.Signature,
		b.MerkleRoot,
		b.NewKeys,
		b.NextMiner)
	h := sha256.New()
	h.Write([]byte(record))
	hashed := h.Sum(nil)
	return hex.EncodeToString(hashed)
}

func (b *Block) VerifySignature() bool {
	// Implement signature verification logic
	// This would typically involve public key cryptography
	return true // Placeholder
}

// String representation of Transaction
func (t Transaction) String() string {
	return fmt.Sprintf("Sender: %d, Recipient: %s, Amount: %d, Nonce: %d",
		t.Sender, t.Recipient, t.Amount, t.Nonce)
}

// String representation of Block
func (b Block) String() string {
	return fmt.Sprintf("Index: %d\nPrevHash: %s\nHash: %s\nTimestamp: %d\nTransactions: %v\nSignature: %s\nNextMiner: %s",
		b.Index, b.PreviousHash, b.Hash, b.Timestamp, b.Transactions, b.Signature, b.NextMiner)
}

func (b *Block) GetIndex() int {
	return b.Index
}

func (b *Block) GetPreviousHash() string {
	return b.PreviousHash
}

func (b *Block) GetTimestamp() int64 {
	return b.Timestamp
}

func (b *Block) GetSignature() string {
	return b.Signature
}

func (b *Block) GetTransactionList() []Transaction {
	return b.Transactions
}

func (b *Block) GetMerkleRoot() []string {
	return b.MerkleRoot
}

func (b *Block) GetNewKeys() []string {
	return b.NewKeys
}

func (b *Block) GetNextMiner() string {
	return b.NextMiner
}

func (b *Block) AddTransaction(transaction Transaction) {
	b.Transactions = append(b.Transactions, transaction)
}
