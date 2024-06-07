// SPDX-License-Identifier: GPL-2.0-or-later

package ffmpeg

import (
	"bufio"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// LogFunc used to log stdout and stderr.
type LogFunc func(string)

// Process interface only used for testing.
type Process interface {
	// Set timeout for process to exit after being stopped.
	Timeout(time.Duration) Process

	// Set function called on stdout line.
	StdoutLogger(LogFunc) Process

	// Set function called on stderr line.
	StderrLogger(LogFunc) Process

	// Start process with context.
	Start(ctx context.Context) error

	// Stop process.
	Stop()
}

// process manages subprocesses.
type process struct {
	timeout time.Duration
	cmd     *exec.Cmd

	stdoutLogger LogFunc
	stderrLogger LogFunc

	done chan struct{}
}

// NewProcessFunc is used for mocking.
type NewProcessFunc func(*exec.Cmd) Process

// NewProcess return process.
func NewProcess(cmd *exec.Cmd) Process {
	return process{
		timeout: 1000 * time.Millisecond,
		cmd:     cmd,
	}
}

func (p process) Timeout(timeout time.Duration) Process {
	p.timeout = timeout
	return p
}

func (p process) StdoutLogger(l LogFunc) Process {
	p.stdoutLogger = l
	return p
}

func (p process) StderrLogger(l LogFunc) Process {
	p.stderrLogger = l
	return p
}

func (p process) Start(ctx context.Context) error {
	if p.stdoutLogger != nil {
		pipe, err := p.cmd.StdoutPipe()
		if err != nil {
			return err
		}
		p.attachLogger(p.stdoutLogger, "stdout", pipe)
	}
	if p.stderrLogger != nil {
		pipe, err := p.cmd.StderrPipe()
		if err != nil {
			return err
		}
		p.attachLogger(p.stderrLogger, "stderr", pipe)
	}

	if err := p.cmd.Start(); err != nil {
		return err
	}

	p.done = make(chan struct{})

	go func() {
		select {
		case <-p.done:
		case <-ctx.Done():
			p.Stop()
		}
	}()

	err := p.cmd.Wait()
	close(p.done)

	// FFmpeg seems to return 255 on normal exit.
	if err != nil && err.Error() == "exit status 255" {
		return nil
	}

	return err
}

func (p process) attachLogger(logFunc LogFunc, label string, pipe io.ReadCloser) {
	scanner := bufio.NewScanner(pipe)
	go func() {
		for scanner.Scan() {
			msg := fmt.Sprintf("%v: %v", label, scanner.Text())
			logFunc(msg)
		}
	}()
}

// Note, can't use CommandContext to Stop process as it would
// kill the process before it has a chance to exit on its own.
func (p process) Stop() {
	p.cmd.Process.Signal(os.Interrupt) //nolint:errcheck

	select {
	case <-p.done:
	case <-time.After(p.timeout):
		p.cmd.Process.Signal(os.Kill) //nolint:errcheck
		<-p.done
	}
}

// FFMPEG stores ffmpeg binary location.
type FFMPEG struct {
	command func(...string) *exec.Cmd
}

// New returns FFMPEG.
func New(bin string) *FFMPEG {
	command := func(args ...string) *exec.Cmd {
		return exec.Command(bin, args...)
	}
	return &FFMPEG{command: command}
}

/*
func HWaccels(bin string) ([]string, error) {
	cmd := exec.Command(bin, "-hwaccels")

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return []string{}, fmt.Errorf("%v", err)
	}

	// Input
	//   accels Hardware acceleration methods:
	//   vdpau
	//   vaapi

	// Output ["vdpau", "vaapi"]
	input := strings.TrimSpace(stdout.String())
	lines := strings.Split(input, "\n")

	return lines[1:], nil
}
*/

// Rect top, left, bottom, right.
type Rect [4]int

// Point on image.
type Point [2]int

// Polygon slice of Points.
type Polygon []Point

// ToAbs returns polygon converted from percentage values to absolute values.
func (p Polygon) ToAbs(w, h int) Polygon {
	polygon := make(Polygon, len(p))
	for i, point := range p {
		px := point[0]
		py := point[1]
		polygon[i] = [2]int{
			int(float64(w) * (float64(px) / 100)),
			int(float64(h) * (float64(py) / 100)),
		}
	}
	return polygon
}

// CreateMask creates an image mask from a polygon.
// Pixels inside the polygon are masked.
func CreateMask(w int, h int, poly Polygon) image.Image {
	img := image.NewAlpha(image.Rect(0, 0, w, h))

	for y := 0; y < w; y++ {
		for x := 0; x < h; x++ {
			if VertexInsidePoly(y, x, poly) {
				img.Set(y, x, color.Alpha{255})
			} else {
				img.Set(y, x, color.Alpha{0})
			}
		}
	}
	return img
}

// CreateInvertedMask creates an image mask from a polygon.
// Pixels outside the polygon are masked.
func CreateInvertedMask(w int, h int, poly Polygon) image.Image {
	img := image.NewAlpha(image.Rect(0, 0, w, h))

	for y := 0; y < w; y++ {
		for x := 0; x < h; x++ {
			if VertexInsidePoly(y, x, poly) {
				img.Set(y, x, color.Alpha{0})
			} else {
				img.Set(y, x, color.Alpha{255})
			}
		}
	}
	return img
}

// VertexInsidePoly returns true if point is inside polygon.
func VertexInsidePoly(y int, x int, poly Polygon) bool {
	inside := false
	j := len(poly) - 1
	for i := 0; i < len(poly); i++ {
		xi := poly[i][0]
		yi := poly[i][1]
		xj := poly[j][0]
		yj := poly[j][1]

		if ((yi > x) != (yj > x)) && (y < (xj-xi)*(x-yi)/(yj-yi)+xi) {
			inside = !inside
		}
		j = i
	}
	return inside
}

// SaveImage saves image to specified location.
func SaveImage(path string, img image.Image) error {
	os.Remove(path)

	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	err = png.Encode(file, img)
	if err != nil {
		return err
	}

	err = file.Close()
	if err != nil {
		return err
	}
	return nil
}

// ParseArgs slices arguments.
func ParseArgs(args string) []string {
	return strings.Split(strings.TrimSpace(args), " ")
}

// ParseScaleString converts string to number that's used in the FFmpeg scale filter.
func ParseScaleString(scale string) string {
	switch strings.ToLower(scale) {
	case "full":
		return "1"
	case "half":
		return "2"
	case "third":
		return "3"
	case "quarter":
		return "4"
	case "sixth":
		return "6"
	case "eighth":
		return "8"
	default:
		return ""
	}
}

// FeedRateToDuration calculates frame duration from feed rate (fps).
func FeedRateToDuration(feedRate float64) time.Duration {
	frameDuration := 1 / feedRate
	return time.Duration(frameDuration * float64(time.Second))
}

// ParseTimestampOffset converts the timestampOffset string to duration.
func ParseTimestampOffset(timestampOffsetStr string) (time.Duration, error) {
	if timestampOffsetStr == "" {
		return 0, nil
	}
	timestampOffsetFloat, err := strconv.Atoi(timestampOffsetStr)
	if err != nil {
		return 0, fmt.Errorf("parse timestamp offset %w", err)
	}
	return time.Duration(timestampOffsetFloat) * time.Millisecond, nil
}
