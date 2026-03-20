package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/charmbracelet/fang"
	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/effects"
	"github.com/gopxl/beep/v2/mp3"
	"github.com/gopxl/beep/v2/speaker"
	"github.com/spf13/cobra"
	"golang.org/x/sys/windows"
)

var version = "dev"

//go:embed audio/pain/*.mp3
var painAudio embed.FS

//go:embed audio/sexy/*.mp3
var sexyAudio embed.FS

//go:embed audio/halo/*.mp3
var haloAudio embed.FS

//go:embed audio/anime/*.mp3
var animeAudio embed.FS

var (
	sexyMode      bool
	haloMode      bool
	animeMode     bool
	customPath    string
	customFiles   []string
	fastMode      bool
	minAmplitude  float64
	cooldownMs    int
	stdioMode     bool
	volumeScaling bool
	paused        bool
	pausedMu      sync.RWMutex
	speedRatio    float64
)

type playMode int

const (
	modeRandom playMode = iota
	modeEscalation
)

const (
	// decayHalfLife is how many seconds of inactivity before intensity
	// halves. Controls how fast escalation fades.
	decayHalfLife = 30.0

	// defaultMinAmplitude is the default detection threshold.
	defaultMinAmplitude = 0.05

	// defaultCooldownMs is the default cooldown between audio responses.
	defaultCooldownMs = 750

	// defaultSensorPollInterval is how often we check for new accelerometer data.
	defaultSensorPollInterval = 10 * time.Millisecond

	// defaultMaxSampleBatch caps the number of accelerometer samples processed
	// per tick to avoid falling behind.
	defaultMaxSampleBatch = 200
)

type runtimeTuning struct {
	minAmplitude float64
	cooldown     time.Duration
	pollInterval time.Duration
	maxBatch     int
}

func defaultTuning() runtimeTuning {
	return runtimeTuning{
		minAmplitude: defaultMinAmplitude,
		cooldown:     time.Duration(defaultCooldownMs) * time.Millisecond,
		pollInterval: defaultSensorPollInterval,
		maxBatch:     defaultMaxSampleBatch,
	}
}

func applyFastOverlay(base runtimeTuning) runtimeTuning {
	base.pollInterval = 4 * time.Millisecond
	base.cooldown = 350 * time.Millisecond
	if base.minAmplitude > 0.18 {
		base.minAmplitude = 0.18
	}
	if base.maxBatch < 320 {
		base.maxBatch = 320
	}
	return base
}

type soundPack struct {
	name   string
	fs     embed.FS
	dir    string
	mode   playMode
	files  []string
	custom bool
}

func (sp *soundPack) loadFiles() error {
	if sp.custom {
		entries, err := os.ReadDir(sp.dir)
		if err != nil {
			return err
		}
		sp.files = make([]string, 0, len(entries))
		for _, entry := range entries {
			if !entry.IsDir() {
				sp.files = append(sp.files, sp.dir+"/"+entry.Name())
			}
		}
	} else {
		entries, err := sp.fs.ReadDir(sp.dir)
		if err != nil {
			return err
		}
		sp.files = make([]string, 0, len(entries))
		for _, entry := range entries {
			if !entry.IsDir() {
				sp.files = append(sp.files, sp.dir+"/"+entry.Name())
			}
		}
	}
	sort.Strings(sp.files)
	if len(sp.files) == 0 {
		return fmt.Errorf("no audio files found in %s", sp.dir)
	}
	return nil
}

type slapTracker struct {
	mu       sync.Mutex
	score    float64
	lastTime time.Time
	total    int
	halfLife float64 // seconds
	scale    float64 // controls the escalation curve shape
	pack     *soundPack
}

func newSlapTracker(pack *soundPack, cooldown time.Duration) *slapTracker {
	// scale maps the exponential curve so that sustained max-rate
	// slapping (one per cooldown) reaches the final file. At steady
	// state the score converges to ssMax; we set scale so that score
	// maps to the last index.
	cooldownSec := cooldown.Seconds()
	ssMax := 1.0 / (1.0 - math.Pow(0.5, cooldownSec/decayHalfLife))
	scale := (ssMax - 1) / math.Log(float64(len(pack.files)+1))
	return &slapTracker{
		halfLife: decayHalfLife,
		scale:    scale,
		pack:     pack,
	}
}

func (st *slapTracker) record(now time.Time) (int, float64) {
	st.mu.Lock()
	defer st.mu.Unlock()

	if !st.lastTime.IsZero() {
		elapsed := now.Sub(st.lastTime).Seconds()
		st.score *= math.Pow(0.5, elapsed/st.halfLife)
	}
	st.score += 1.0
	st.lastTime = now
	st.total++
	return st.total, st.score
}

func (st *slapTracker) getFile(score float64) string {
	if st.pack.mode == modeRandom {
		return st.pack.files[rand.Intn(len(st.pack.files))]
	}

	// Escalation: 1-exp(-x) curve maps score to file index.
	// At sustained max slap rate, score reaches ssMax which maps
	// to the final file.
	maxIdx := len(st.pack.files) - 1
	idx := min(int(float64(len(st.pack.files))*(1.0-math.Exp(-(score-1)/st.scale))), maxIdx)
	return st.pack.files[idx]
}

func main() {
	cmd := &cobra.Command{
		Use:   "spank",
		Short: "Yells 'ow!' when you slap the laptop",
		Long: `spank reads the Apple Silicon accelerometer directly via IOKit HID
and plays audio responses when a slap or hit is detected.

Requires sudo (for IOKit HID access to the accelerometer).

Use --sexy for a different experience. In sexy mode, the more you slap
within a minute, the more intense the sounds become.

Use --halo to play random audio clips from Halo soundtracks on each slap.`,
		Version: version,
		RunE: func(cmd *cobra.Command, args []string) error {
			tuning := defaultTuning()
			if fastMode {
				tuning = applyFastOverlay(tuning)
			}
			// Explicit flags override fast preset defaults
			if cmd.Flags().Changed("min-amplitude") {
				tuning.minAmplitude = minAmplitude
			}
			if cmd.Flags().Changed("cooldown") {
				tuning.cooldown = time.Duration(cooldownMs) * time.Millisecond
			}
			return run(cmd.Context(), tuning)
		},
		SilenceUsage: true,
	}

	cmd.Flags().BoolVarP(&sexyMode, "sexy", "s", false, "Enable sexy mode")
	cmd.Flags().BoolVarP(&animeMode, "anime", "a", false, "Enable anime mode")
	cmd.Flags().BoolVarP(&haloMode, "halo", "H", false, "Enable halo mode")

	if err := fang.Execute(context.Background(), cmd); err != nil {
		os.Exit(1)
	}
}

func run(ctx context.Context, tuning runtimeTuning) error {
	modeCount := 0
	if sexyMode {
		modeCount++
	}
	if haloMode {
		modeCount++
	}
	if animeMode {
		modeCount++
	}
	if customPath != "" || len(customFiles) > 0 {
		modeCount++
	}
	if modeCount > 1 {
		return fmt.Errorf("--sexy, --halo, --anime and --custom/--custom-files are mutually exclusive; pick one")
	}

	if tuning.minAmplitude < 0 || tuning.minAmplitude > 1 {
		return fmt.Errorf("--min-amplitude must be between 0.0 and 1.0")
	}
	if tuning.cooldown <= 0 {
		return fmt.Errorf("--cooldown must be greater than 0")
	}

	var pack *soundPack
	switch {
	case len(customFiles) > 0:
		// Validate all files exist and are MP3s
		for _, f := range customFiles {
			if !strings.HasSuffix(strings.ToLower(f), ".mp3") {
				return fmt.Errorf("custom file must be MP3: %s", f)
			}
			if _, err := os.Stat(f); err != nil {
				return fmt.Errorf("custom file not found: %s", f)
			}
		}
		pack = &soundPack{name: "custom", mode: modeRandom, custom: true, files: customFiles}
	case customPath != "":
		pack = &soundPack{name: "custom", dir: customPath, mode: modeRandom, custom: true}
	case sexyMode:
		pack = &soundPack{name: "sexy", fs: sexyAudio, dir: "audio/sexy", mode: modeEscalation}
	case animeMode:
		pack = &soundPack{name: "anime", fs: animeAudio, dir: "audio/anime", mode: modeRandom}
	case haloMode:
		pack = &soundPack{name: "halo", fs: haloAudio, dir: "audio/halo", mode: modeRandom}
	default:
		pack = &soundPack{name: "pain", fs: painAudio, dir: "audio/pain", mode: modeRandom}
	}

	// Only load files if not already set (customFiles case)
	if len(pack.files) == 0 {
		if err := pack.loadFiles(); err != nil {
			return fmt.Errorf("loading %s audio: %w", pack.name, err)
		}
	}

	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	return listenForSlaps(ctx, pack, tuning)
}

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	procGetCursorPos     = user32.NewProc("GetCursorPos")
	procGetAsyncKeyState = user32.NewProc("GetAsyncKeyState")
)

type POINT struct {
	X int32
	Y int32
}

func getCursorPos() POINT {
	var pt POINT
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	return pt
}

func leftTap() bool {
	r, _, _ := procGetAsyncKeyState.Call(0x01) // VK_LBUTTON
	return r&0x8000 != 0
}

func listenForSlaps(ctx context.Context, pack *soundPack, tuning runtimeTuning) error {

	tracker := newSlapTracker(pack, tuning.cooldown)

	var last POINT
	var lastYell time.Time
	var lastTapState bool

	last = getCursorPos()

	fmt.Printf("spank: listening for mouse shakes + taps in %s mode...\n", pack.name)

	ticker := time.NewTicker(tuning.pollInterval)
	defer ticker.Stop()

	for {

		select {
		case <-ctx.Done():
			fmt.Println("\nbye!")
			return nil
		case <-ticker.C:
		}

		pausedMu.RLock()
		isPaused := paused
		pausedMu.RUnlock()

		if isPaused {
			continue
		}

		now := time.Now()

		cur := getCursorPos()

		dx := float64(cur.X - last.X)
		dy := float64(cur.Y - last.Y)

		amplitude := math.Sqrt(dx*dx+dy*dy) * 2

		shake := amplitude > 10

		tapState := leftTap()
		tap := tapState && !lastTapState
		lastTapState = tapState

		if !shake && !tap {
			last = cur
			continue
		}

		if time.Since(lastYell) < tuning.cooldown {
			last = cur
			continue
		}

		lastYell = now

		num, score := tracker.record(now)
		file := tracker.getFile(score)

		if stdioMode {

			event := map[string]interface{}{
				"timestamp":  now.Format(time.RFC3339Nano),
				"slapNumber": num,
				"amplitude":  amplitude,
				"severity":   "motion",
				"file":       file,
			}

			data, _ := json.Marshal(event)
			fmt.Println(string(data))

		} else {

			fmt.Printf(
				"slap #%d [amp=%.3f] -> %s\n",
				num,
				amplitude,
				file,
			)
		}

		go playAudio(pack, file, amplitude, new(bool))

		last = cur
	}
}

var speakerMu sync.Mutex

// amplitudeToVolume maps a detected amplitude to a beep/effects.Volume
// level. Amplitude typically ranges from ~0.05 (light tap) to ~1.0+
// (hard slap). The mapping uses a logarithmic curve so that light taps
// are noticeably quieter and hard hits play near full volume.
//
// Returns a value in the range [-3.0, 0.0] for use with effects.Volume
// (base 2): -3.0 is ~1/8 volume, 0.0 is full volume.
func amplitudeToVolume(amplitude float64) float64 {
	const (
		minAmp = 0.05 // softest detectable
		maxAmp = 0.80 // treat anything above this as max
		minVol = -3.0 // quietest playback (1/8 volume with base 2)
		maxVol = 0.0  // full volume
	)

	// Clamp
	if amplitude <= minAmp {
		return minVol
	}
	if amplitude >= maxAmp {
		return maxVol
	}

	// Normalize to [0, 1]
	t := (amplitude - minAmp) / (maxAmp - minAmp)

	// Log curve for more natural volume scaling
	// log(1 + t*99) / log(100) maps [0,1] -> [0,1] with a log curve
	t = math.Log(1+t*99) / math.Log(100)

	return minVol + t*(maxVol-minVol)
}

func playAudio(pack *soundPack, path string, amplitude float64, speakerInit *bool) {
	var streamer beep.StreamSeekCloser
	var format beep.Format

	if pack.custom {
		file, err := os.Open(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "spank: open %s: %v\n", path, err)
			return
		}
		defer file.Close()
		streamer, format, err = mp3.Decode(file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "spank: decode %s: %v\n", path, err)
			return
		}
	} else {
		data, err := pack.fs.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "spank: read %s: %v\n", path, err)
			return
		}
		streamer, format, err = mp3.Decode(io.NopCloser(bytes.NewReader(data)))
		if err != nil {
			fmt.Fprintf(os.Stderr, "spank: decode %s: %v\n", path, err)
			return
		}
	}
	defer streamer.Close()

	speakerMu.Lock()
	if !*speakerInit {
		speaker.Init(format.SampleRate, format.SampleRate.N(time.Second/10))
		*speakerInit = true
	}
	speakerMu.Unlock()

	// Optionally scale volume based on slap amplitude
	var source beep.Streamer = streamer
	if volumeScaling {
		source = &effects.Volume{
			Streamer: streamer,
			Base:     2,
			Volume:   amplitudeToVolume(amplitude),
			Silent:   false,
		}
	}

	// Apply speed change via resampling trick:
	// Claiming the audio is at rate*speed and resampling back to rate
	// makes the speaker consume samples faster/slower.
	if speedRatio != 1.0 && speedRatio > 0 {
		fakeRate := beep.SampleRate(int(float64(format.SampleRate) * speedRatio))
		source = beep.Resample(4, fakeRate, format.SampleRate, source)
	}

	done := make(chan bool)
	speaker.Play(beep.Seq(source, beep.Callback(func() {
		done <- true
	})))
	<-done
}
