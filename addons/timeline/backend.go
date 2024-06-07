// SPDX-License-Identifier: GPL-2.0-or-later

package timeline

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"nvr"
	"nvr/pkg/ffmpeg"
	"nvr/pkg/log"
	"nvr/pkg/monitor"
	"nvr/pkg/storage"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func init() {
	nvr.RegisterLogSource([]string{"timeline"})
	nvr.RegisterMonitorRecSavedHook(onRecSaved)
	nvr.RegisterMigrationMonitorHook(migrate)

	nvr.RegisterTplSubHook(modifySubTemplates)
	nvr.RegisterTplHook(modifyTemplates)

	nvr.RegisterAppRunHook(func(_ context.Context, app *nvr.App) error {
		app.Router.Handle(
			"/api/recording/timeline/",
			app.Auth.User(handleTimeline(app.Env.RecordingsDir())),
		)
		app.Router.Handle(
			"/timeline",
			app.Auth.User(app.Templater.Render("timeline.tpl")),
		)
		app.Router.Handle(
			"/timeline.mjs",
			app.Auth.User(serveTimelineMjs()),
		)
		return nil
	})
}

func handleTimeline(recordingsDir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "invalid request method", http.StatusMethodNotAllowed)
			return
		}

		recID := r.URL.Path[24:] // Trim "/api/recording/timeline/"
		timelinePath, err := storage.RecordingIDToPath(recID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		path := filepath.Join(recordingsDir, timelinePath+".timeline")

		// ServeFile will sanitize ".."
		http.ServeFile(w, r, path)
	})
}

func onRecSaved(r *monitor.Recorder, recPath string, recData storage.RecordingData) {
	id := r.Config.ID()
	logf := func(level log.Level, format string, a ...interface{}) {
		msg := fmt.Sprintf(format, a...)
		r.Logger.Log(log.Entry{
			Level:     level,
			Src:       "timeline",
			MonitorID: id,
			Msg:       r.Env.CensorLog(msg),
		})
	}

	err := recSaved(r, logf, recPath, recData)
	if err != nil {
		logf(log.LevelError, err.Error())
	}
}

func recSaved(
	r *monitor.Recorder,
	logf log.Func,
	recPath string,
	recData storage.RecordingData,
) error {
	config, err := parseConfig(r.Config)
	if err != nil {
		return fmt.Errorf("could not parse config: %w", err)
	}

	video, err := storage.NewVideoReader(recPath, nil)
	if err != nil {
		return fmt.Errorf("video reader: %w", err)
	}
	defer video.Close()

	tempPath := recPath + ".timeline_tmp"
	timelinePath := recPath + ".timeline"

	args := genArgs(r.Config.LogLevel(), tempPath, *config)

	logf(log.LevelInfo, "generating: %v", strings.Join(args, " "))
	cmd := exec.Command(r.Env.FFmpegBin, args...)
	cmd.Stdin = video

	logFunc := func(msg string) {
		logf(log.FFmpegLevel(r.Config.LogLevel()), "process: %v", msg)
	}

	process := r.NewProcess(cmd).
		StdoutLogger(logFunc).
		StderrLogger(logFunc)

	recDuration := recData.End.Sub(recData.Start)
	ctx, cancel := context.WithTimeout(context.Background(), recDuration)
	defer cancel()

	if err := process.Start(ctx); err != nil {
		return fmt.Errorf("could not generate video: %w %v", err, args)
	}

	if err := os.Rename(tempPath, timelinePath); err != nil {
		return fmt.Errorf("could not rename temp file: %w", err)
	}
	logf(log.LevelInfo, "done: %v", filepath.Base(timelinePath))

	return nil
}

const defaultScale = "8"

func genArgs(logLevel string, outputPath string, c config) []string {
	scale := ffmpeg.ParseScaleString(c.scale)
	if scale == "" {
		scale = defaultScale
	}
	crf := parseQuality(c.quality)
	fps := parseFrameRate(c.frameRate)

	args := []string{
		"-n", "-loglevel", logLevel,
		"-threads", "1", "-discard", "nokey",
		"-i", "-", "-an",
		"-c:v", "libx264", "-x264-params", "keyint=4",
		"-preset", "veryfast", "-tune", "fastdecode", "-crf", crf,
		"-vsync", "vfr", "-vf",
	}

	filters := "mpdecimate,fps=" + fps + ",mpdecimate"
	if scale != "1" {
		filters += ",scale='iw/" + scale + ":ih/" + scale + "'"
	}

	args = append(args, filters)

	args = append(args, "-movflags", "empty_moov+default_base_moof+frag_keyframe")
	args = append(args, "-f", "mp4", outputPath)

	return args
}

func parseQuality(q string) string {
	switch q {
	case "1":
		return "18"
	case "2":
		return "21"
	case "3":
		return "24"
	case "4":
		return "27"
	case "5":
		return "30"
	case "6":
		return "33"
	case "7":
		return "36"
	case "8":
		return "39"
	case "9":
		return "42"
	case "10":
		return "45"
	case "11":
		return "48"
	case "12":
		return "51"
	}
	return "27"
}

const defaultFrameRate = "6"

func parseFrameRate(rate string) string {
	fpm, err := strconv.ParseFloat(rate, 64)
	if err != nil || fpm <= 0 {
		return defaultFrameRate
	}

	fps := fpm / 60
	return strconv.FormatFloat(fps, 'f', 4, 32)
}

type config struct {
	scale     string
	quality   string
	frameRate string
}

type rawConfigV1 struct {
	Scale     string `json:"scale"`
	Quality   string `json:"quality"`
	FrameRate string `json:"frameRate"`
}

func parseConfig(conf monitor.Config) (*config, error) {
	var rawConf rawConfigV1
	rawTimeline := conf.Get("timeline")
	if rawTimeline != "" {
		err := json.Unmarshal([]byte(rawTimeline), &rawConf)
		if err != nil {
			return nil, fmt.Errorf("unmarshal doods: %w", err)
		}
	}
	return &config{
		scale:     rawConf.Scale,
		quality:   rawConf.Quality,
		frameRate: rawConf.FrameRate,
	}, nil
}

const currentConfigVersion = 1

func migrate(c monitor.RawConfig) error {
	configVersion, _ := strconv.Atoi(c["timelineConfigVersion"])

	if configVersion < 1 {
		if err := migrateV0toV1(c); err != nil {
			return fmt.Errorf("timeline v0 to v1: %w", err)
		}
	}

	c["timelineConfigVersion"] = strconv.Itoa(currentConfigVersion)
	return nil
}

func migrateV0toV1(c monitor.RawConfig) error {
	config := rawConfigV1{
		Scale:     c["timelineScale"],
		Quality:   c["timelineQuality"],
		FrameRate: c["timelineFrameRate"],
	}

	delete(c, "timelineScale")
	delete(c, "timelineQuality")
	delete(c, "timelineFrameRate")

	rawConfig, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("marshal raw config: %w", err)
	}
	c["timeline"] = string(rawConfig)
	return nil
}
