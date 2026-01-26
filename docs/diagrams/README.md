# Architecture Diagrams

This directory contains Mermaid source files for df2redis architecture diagrams.

## Files

### Replication Protocol Sequence Diagrams
- `replication-protocol-zh.mmd` - Chinese version (复制协议时序图)
- `replication-protocol-en.mmd` - English version

### State Machine Diagrams
- `state-machine-diagram-zh.mmd` - Chinese version (副本状态机图)
- `state-machine-diagram-en.mmd` - English version

## Generating Diagrams

### Prerequisites

Install mermaid-cli:

```bash
npm install -g @mermaid-js/mermaid-cli
```

### Quick Start - Generate All Diagrams

```bash
# From project root directory
cd docs/diagrams

# Run the generation script (generates both SVG and PNG)
./generate.sh
```

This will generate:
- 4 SVG files (Chinese + English for both diagrams)
- 4 PNG files (Chinese + English for both diagrams)
- 4 default files (copies of Chinese versions)

### Recommended Dimensions

Based on analysis of production SVG files (e.g., Dragonfly's 2650×1417 throughput chart), we use:

| Diagram Type | Width | Height | Aspect Ratio | Reason |
|-------------|-------|--------|--------------|--------|
| **Replication Protocol** | 2800 | 3200 | 0.875:1 | Vertical sequence diagram with 5 phases |
| **State Machine** | 2400 | 3600 | 0.67:1 | Vertical state flow with nested states |

**Why these sizes?**
- ✅ High resolution for Retina/4K displays
- ✅ Clear text rendering at all zoom levels
- ✅ Suitable for both web and print (300 DPI equivalent)
- ✅ SVG scales infinitely, dimensions are reference baseline

### Generate Individual Diagrams

#### SVG Format (Recommended for Web)

```bash
# Replication protocol (Chinese)
mmdc -i replication-protocol-zh.mmd \
     -o ../images/architecture/replication-protocol-zh.svg \
     -w 2800 -H 3200 -b transparent

# State machine (English)
mmdc -i state-machine-diagram-en.mmd \
     -o ../images/architecture/state-machine-diagram-en.svg \
     -w 2400 -H 3600 -b transparent
```

#### PNG Format (For Compatibility)

```bash
# Replication protocol (Chinese)
mmdc -i replication-protocol-zh.mmd \
     -o ../images/architecture/replication-protocol-zh.png \
     -w 2800 -H 3200 -b transparent

# State machine (English)
mmdc -i state-machine-diagram-en.mmd \
     -o ../images/architecture/state-machine-diagram-en.png \
     -w 2400 -H 3600 -b transparent
```

### Generate Individual Diagrams

```bash
# Replication protocol (Chinese)
mmdc -i replication-protocol-zh.mmd \
     -o ../images/architecture/replication-protocol-zh.png \
     -w 2400 -H 3200 -b transparent

# State machine (English)
mmdc -i state-machine-diagram-en.mmd \
     -o ../images/architecture/state-machine-diagram-en.png \
     -w 2600 -H 3600 -b transparent
```

### Parameters Explained

- `-i` : Input mermaid file
- `-o` : Output file path (.svg or .png)
- `-w` : Width in pixels (reference size for SVG, actual size for PNG)
- `-H` : Height in pixels (reference size for SVG, actual size for PNG)
- `-b transparent` : Transparent background

### SVG vs PNG - Which to Use?

| Format | File Size | Scaling | Use Case |
|--------|-----------|---------|----------|
| **SVG** | ~50-100 KB | ♾️ Infinite, no quality loss | • GitHub README<br>• Web documentation<br>• Interactive docs |
| **PNG** | ~200-500 KB | ❌ Pixelated when enlarged | • PDF exports<br>• Presentations<br>• Email attachments |

**Recommendation**: Use SVG for online documentation, PNG as fallback for compatibility.

## Online Preview

You can preview and edit these diagrams online:

1. Visit [Mermaid Live Editor](https://mermaid.live/)
2. Copy the content of any `.mmd` file
3. Paste into the editor
4. Download as PNG or SVG

## Color Scheme

| Phase/Module | Color | RGB | Description |
|-------------|-------|-----|-------------|
| 🤝 Handshake | Light Blue | `rgb(220, 237, 250)` | Connection establishment |
| 🔌 FLOW Registration | Light Green | `rgb(220, 250, 230)` | Channel creation |
| 📦 Full Sync | Light Orange | `rgb(255, 243, 224)` | RDB transfer |
| ⚠️ EOF Token | Light Red | `rgb(255, 230, 230)` | Critical step |
| 🚧 Global Barrier | Light Purple | `rgb(243, 229, 245)` | Synchronization point |
| 🔄 Stable Sync | Light Cyan | `rgb(224, 247, 250)` | Continuous operation |
| ❌ Error Handling | Pink | `fill:#ffebee` | Error paths |
| 🔴 Critical Node | Dark Red | `stroke-width:3px` | Important focus |

## Editing Guidelines

When updating diagrams:

1. **Maintain consistency** between Chinese and English versions
2. **Keep color coding** consistent across all diagrams
3. **Update both versions** when making changes
4. **Test generation** before committing
5. **Update documentation** if protocol changes

## Version History

- **2025-01-26**: Initial version with 5-phase replication protocol
  - Added EOF Token handling
  - Added graceful shutdown process
  - Removed Dragonfly v1.36.0 bug workaround references
