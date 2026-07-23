#!/usr/bin/env bash
set -e

MODEL="${1:-gemma4:e2b}"
echo "=== Installing Pre-Trained Open Model: $MODEL ==="
if command -v ollama >/dev/null 2>&1; then
    echo "Downloading model via Ollama CLI..."
    ollama pull "$MODEL"
else
    echo "Sending pull request to local Ollama server at 127.0.0.1:11434..."
    curl -s -X POST http://127.0.0.1:11434/api/pull -d "{\"name\": \"$MODEL\", \"stream\": false}"
fi
echo "✅ Model $MODEL successfully downloaded and ready for inferencing!"
