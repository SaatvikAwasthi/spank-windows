# spank-windows
Shake your PC, it yells back. Why should mac have all the fun!!

Uses the mouse movements to detect hits on your laptop and plays audio responses. Single binary, no dependencies.

## Install

Download from the [latest release](https://github.com/SaatvikAwasthi/spank-windows/releases/latest).

Or build from source:

```bash
go install github.com/SaatvikAwasthi/spank-windows@latest
```

windows

```bash
go build -o spank.exe
```

linux

```bash
GOOS=darwin GOARCH=amd64 go build -o app
```

macos

```bash
GOOS=linux GOARCH=amd64 go build -o app
```


## Requirements

- laptop with trackpad or mouse
- Go 1.26+ (if building from source)

## Usage

```bash
# Normal mode — says "ow!" when slapped
spank

# Sexy mode — escalating responses based on slap frequency
spank --sexy

# Halo mode — plays Halo death sounds when slapped
spank --halo

# Anime mode — plays Halo death sounds when slapped
spank --anime
```

### Modes

**Pain mode** (default): Randomly plays from 10 pain/protest audio clips when a slap is detected.

**Sexy mode** (`--sexy`): Tracks slaps within a rolling 5-minute window. The more you slap, the more intense the audio response. 60 levels of escalation.

**Halo mode** (`--halo`): Randomly plays from death sound effects from the Halo video game series when a slap is detected.

**Anime mode** (`--anime`): Randomly plays MP3 files from a anime directory.

## How it works

1. Reads mouse movements and left click from trackpad and pointer devices.
2. Runs vibration detection (STA/LTA, CUSUM, kurtosis, peak/MAD)
3. When a significant impact is detected, plays an embedded MP3 response

## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=SaatvikAwasthi/spank-windows&type=date&legend=top-left)](https://www.star-history.com/#SaatvikAwasthi/spank-windows&type=date&legend=top-left)

## Credits

Base code inspiration from [taigrr/spank](https://github.com/taigrr/spank) for mac os

## License

MIT