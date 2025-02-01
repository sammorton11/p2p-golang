#!/bin/bash

# Kill any existing processes on these ports first
kill $(lsof -t -i:6666) 2>/dev/null
kill $(lsof -t -i:6667) 2>/dev/null
kill $(lsof -t -i:6668) 2>/dev/null

# Start each node in a new Alacritty window
alacritty --working-directory "$(pwd)" -e bash -c "go run main.go blockchain.go -p 6666 -bootstrap" &
sleep 2

alacritty --working-directory "$(pwd)" -e bash -c "go run main.go blockchain.go -p 6667" &
sleep 1

alacritty --working-directory "$(pwd)" -e bash -c "go run main.go blockchain.go -p 6668" &

echo "All nodes started in separate Alacritty terminals!"
