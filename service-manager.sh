#!/bin/bash
# Service management script for knowledge-mcp dependencies
# Usage: ./service-manager.sh {ollama|reranker|all} {start|stop|restart|status|install}

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
OLLAMA_BIN="$SCRIPT_DIR/.ollama_persist/Ollama.app/Contents/Resources/ollama"
OLLAMA_HOME="$SCRIPT_DIR/.ollama_persist/home"
OLLAMA_LOG="$SCRIPT_DIR/.ollama_persist/logs/ollama.log"
OLLAMA_ERR="$SCRIPT_DIR/.ollama_persist/logs/ollama.err"
OLLAMA_PLIST="$SCRIPT_DIR/.ollama_persist/com.ollama.service.plist"

INFINITY_BIN="$SCRIPT_DIR/.venv/bin/infinity_emb"
INFINITY_LOG="$SCRIPT_DIR/.infinity_cache/infinity.log"
INFINITY_ERR="$SCRIPT_DIR/.infinity_cache/infinity.err"
INFINITY_PLIST="$SCRIPT_DIR/.ollama_persist/com.infinity-reranker.service.plist"

LAUNCH_AGENTS_DIR="$HOME/Library/LaunchAgents"

ensure_launch_agents_dir() {
    mkdir -p "$LAUNCH_AGENTS_DIR"
}

# ── launchd management ──────────────────────────────────────

install_plist() {
    local src="$1"
    local name="$2"
    ensure_launch_agents_dir
    cp "$src" "$LAUNCH_AGENTS_DIR/$name"
    echo "  → Installed $name to $LAUNCH_AGENTS_DIR/"
}

load_service() {
    local plist="$LAUNCH_AGENTS_DIR/$1"
    launchctl load "$plist" 2>&1
    echo "  → Loaded $1"
}

unload_service() {
    local plist="$LAUNCH_AGENTS_DIR/$1"
    launchctl unload "$plist" 2>&1 || true
    echo "  → Unloaded $1"
}

# ── nohup fallback management ──────────────────────────────

start_ollama_nohup() {
    echo "  Starting Ollama via nohup..."
    export OLLAMA_HOME="$SCRIPT_DIR/.ollama_persist/home/.ollama"
    export HOME="$SCRIPT_DIR/.ollama_persist/home"
    nohup "$OLLAMA_BIN" serve \
        > "$OLLAMA_LOG" 2> "$OLLAMA_ERR" &
    echo "  → PID $! | log: $OLLAMA_LOG"
}

start_reranker_nohup() {
    echo "  Starting Infinity Reranker via nohup..."
    export HF_HOME="$SCRIPT_DIR/.infinity_cache"
    nohup "$INFINITY_BIN" v2 \
        --model-id Alibaba-NLP/gte-multilingual-reranker-base \
        --port 7997 \
        --device cpu \
        --batch-size 8 \
        > "$INFINITY_LOG" 2> "$INFINITY_ERR" &
    echo "  → PID $! | log: $INFINITY_LOG"
}

stop_service_port() {
    local port="$1"
    local pid
    pid=$(lsof -ti :"$port" 2>/dev/null) && kill "$pid" 2>/dev/null && echo "  → Stopped PID $pid" || echo "  → Nothing running on port $port"
}

check_port() {
    local port="$1"
    if lsof -ti :"$port" 2>/dev/null >/dev/null; then
        echo "  ✅ Port $port is in use"
        return 0
    else
        echo "  ❌ Port $port is free"
        return 1
    fi
}

# ── Commands ────────────────────────────────────────────────

cmd_status() {
    echo "=== Service Status ==="
    echo ""
    echo "--- Ollama (port 11434) ---"
    if curl -s --max-time 3 http://localhost:11434/api/tags >/dev/null 2>&1; then
        echo "  ✅ API responding"
        curl -s http://localhost:11434/api/tags | python3 -c "import sys,json; d=json.load(sys.stdin); [print(f'  Model: {m[\"name\"]}') for m in d.get('models',[])]" 2>/dev/null
    else
        echo "  ❌ Not responding"
    fi
    echo ""
    echo "--- Reranker (port 7997) ---"
    if curl -s --max-time 3 http://localhost:7997/models >/dev/null 2>&1; then
        echo "  ✅ API responding"
    else
        echo "  ❌ Not responding"
    fi
    echo ""
    echo "--- launchd ---"
    launchctl list | grep -E 'ollama|infinity' 2>/dev/null || echo "  (not loaded as launchd service)"
}

cmd_install() {
    echo "=== Installing launchd services ==="
    echo ""
    echo "[1/2] Installing Ollama..."
    install_plist "$OLLAMA_PLIST" "com.ollama.service.plist"
    echo ""
    echo "[2/2] Installing Infinity Reranker..."
    install_plist "$INFINITY_PLIST" "com.infinity-reranker.service.plist"
    echo ""
    echo "✅ Plists installed. Run '$0 all start' to load them."
}

cmd_start() {
    local service="$1"
    case "$service" in
        ollama)
            echo "=== Starting Ollama ==="
            # Try launchd first
            if [ -f "$LAUNCH_AGENTS_DIR/com.ollama.service.plist" ]; then
                load_service "com.ollama.service.plist" || start_ollama_nohup
            else
                start_ollama_nohup
            fi
            sleep 2
            check_port 11434
            ;;
        reranker)
            echo "=== Starting Reranker ==="
            if [ -f "$LAUNCH_AGENTS_DIR/com.infinity-reranker.service.plist" ]; then
                load_service "com.infinity-reranker.service.plist" || start_reranker_nohup
            else
                start_reranker_nohup
            fi
            sleep 3
            check_port 7997
            ;;
        all)
            cmd_start ollama
            echo ""
            cmd_start reranker
            ;;
    esac
}

cmd_stop() {
    local service="$1"
    case "$service" in
        ollama)
            echo "=== Stopping Ollama ==="
            unload_service "com.ollama.service.plist" || true
            stop_service_port 11434
            ;;
        reranker)
            echo "=== Stopping Reranker ==="
            unload_service "com.infinity-reranker.service.plist" || true
            stop_service_port 7997
            ;;
        all)
            cmd_stop ollama
            cmd_stop reranker
            ;;
    esac
}

cmd_restart() {
    local service="$1"
    cmd_stop "$service"
    sleep 1
    cmd_start "$service"
}

# ── Main ────────────────────────────────────────────────────

case "${1:-help}" in
    status)    cmd_status ;;
    install)   cmd_install ;;
    start)     cmd_start "${2:-all}" ;;
    stop)      cmd_stop "${2:-all}" ;;
    restart)   cmd_restart "${2:-all}" ;;
    help|*)
        echo "Usage: $0 {ollama|reranker|all} {start|stop|restart|status}"
        echo "       $0 install    — Install plists to ~/Library/LaunchAgents/"
        echo "       $0 status     — Check all services"
        echo ""
        echo "Examples:"
        echo "  $0 all status       # Check both services"
        echo "  $0 all start        # Start both services"
        echo "  $0 ollama restart   # Restart only Ollama"
        echo "  $0 install          # Install launchd plists"
        ;;
esac
