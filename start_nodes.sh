#!/bin/bash

# Detect available terminal emulator
if command -v alacritty >/dev/null 2>&1; then
    TERM_CMD="alacritty --working-directory"
    EXEC_CMD="-e"
elif command -v gnome-terminal >/dev/null 2>&1; then
    TERM_CMD="gnome-terminal --working-directory"
    EXEC_CMD="--"
elif command -v konsole >/dev/null 2>&1; then
    TERM_CMD="konsole --workdir"
    EXEC_CMD="-e"
elif command -v xfce4-terminal >/dev/null 2>&1; then
    TERM_CMD="xfce4-terminal --working-directory"
    EXEC_CMD="-x"
else
    TERM_CMD="xterm -e cd"
    EXEC_CMD="-e"
fi

# Kill any existing processes on these ports first
kill $(lsof -t -i:6666) 2>/dev/null
kill $(lsof -t -i:6667) 2>/dev/null
kill $(lsof -t -i:6668) 2>/dev/null

# Start each node in a new terminal window
$TERM_CMD "$(pwd)" $EXEC_CMD bash -c "go run main.go blockchain.go -p 6666 -bootstrap" &
sleep 2
$TERM_CMD "$(pwd)" $EXEC_CMD bash -c "go run main.go blockchain.go -p 6667" &
sleep 1
$TERM_CMD "$(pwd)" $EXEC_CMD bash -c "go run main.go blockchain.go -p 6668" &

echo "All nodes started in separate terminals!"
