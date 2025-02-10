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

$TERM_CMD "$(pwd)" $EXEC_CMD bash -c "go run main.go blockchain.go -p 6666 -bootstrap" &
sleep 2
$TERM_CMD "$(pwd)" $EXEC_CMD bash -c "go run main.go blockchain.go -p 6667" &
sleep 1
$TERM_CMD "$(pwd)" $EXEC_CMD bash -c "go run main.go blockchain.go -p 6668" &

# Profiling
$TERM_CMD "$(pwd)" $EXEC_CMD bash -c "go tool pprof -http=:8080 'http://localhost:6069/debug/pprof/heap'" & # Start each node in a new terminal window


echo "All nodes started in separate terminals!"
