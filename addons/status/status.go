// SPDX-License-Identifier: GPL-2.0-or-later

package status

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"nvr"
	"nvr/pkg/log"
	"nvr/pkg/storage"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/mem"
)

func init() {
	var sys *system

	nvr.RegisterAppRunHook(func(ctx context.Context, app *nvr.App) error {
		sys = newSystem(
			app.Storage.DiskUsageCached,
			app.Storage.DiskUsage,
			app.Logger,
		)
		go sys.StatusLoop(ctx)
		return nil
	})

	nvr.RegisterTplDataHook(func(data template.FuncMap, _ string) {
		data["status"] = sys.getStatus()
	})

	nvr.RegisterTplSubHook(modifySubTemplate)
}

type status struct {
	CPUUsage           int    `json:"cpuUsage"`
	RAMUsage           int    `json:"ramUsage"`
	DiskUsage          int    `json:"diskUsage"`
	DiskUsageFormatted string `json:"diskUsageFormatted"`
}

type (
	cpuFunc        func(context.Context, time.Duration, bool) ([]float64, error)
	ramFunc        func() (*mem.VirtualMemoryStat, error)
	diskCachedFunc func() (storage.DiskUsage, time.Duration)
	diskFunc       func(time.Duration) (storage.DiskUsage, error)
)

type system struct {
	cpu        cpuFunc
	ram        ramFunc
	diskCached diskCachedFunc
	disk       diskFunc

	status status

	interval time.Duration

	logf log.Func
	mu   sync.Mutex
}

func newSystem(
	diskCached diskCachedFunc,
	diskUpdate diskFunc,
	logger *log.Logger,
) *system {
	logf := func(level log.Level, format string, a ...interface{}) {
		logger.Log(log.Entry{
			Level: level,
			Src:   "app",
			Msg:   fmt.Sprintf(format, a...),
		})
	}

	return &system{
		cpu:        cpu.PercentWithContext,
		ram:        mem.VirtualMemory,
		diskCached: diskCached,
		disk:       diskUpdate,

		interval: 10 * time.Second,

		logf: logf,
	}
}

func (s *system) updateCPUAndRAM(ctx context.Context) error {
	cpuUsage, err := s.cpu(ctx, s.interval, false)
	if err != nil {
		return fmt.Errorf("get cpu usage %w", err)
	}
	ramUsage, err := s.ram()
	if err != nil {
		return fmt.Errorf("get ram usage %w", err)
	}

	s.mu.Lock()
	s.status.CPUUsage = int(cpuUsage[0])
	s.status.RAMUsage = int(ramUsage.UsedPercent)
	s.mu.Unlock()

	return nil
}

// StatusLoop updates system status until context is canceled.
func (s *system) StatusLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		err := s.updateCPUAndRAM(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			s.logf(log.LevelError, "could not update system status: %v", err)
		}
	}
}

func (s *system) getStatus() status {
	defer s.mu.Unlock()
	s.mu.Lock()

	s.updateDiskUnsafe()

	return s.status
}

const maxAge = 2 * time.Minute

func (s *system) updateDiskUnsafe() {
	diskUsage, age := s.diskCached()
	if age > maxAge {
		go func() {
			_, err := s.disk(maxAge)
			if err != nil {
				s.logf(log.LevelError, "could not get disk usage: %v", err)
			}
		}()
	}

	s.status.DiskUsage = diskUsage.Percent
	s.status.DiskUsageFormatted = diskUsage.Formatted
}

/*func handleStatus(sys *system) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "invalid request method", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(sys.getStatus()); err != nil {
			http.Error(w, "could not encode json", http.StatusInternalServerError)
		}
	})
}*/

func modifySubTemplate(pageFiles map[string]string) error {
	const target = "</aside>"

	pageFiles["sidebar.tpl"] = strings.ReplaceAll(
		pageFiles["sidebar.tpl"],
		target,
		sidebarHTML+target,
	)
	return nil
}

const sidebarHTML = `
	<style>
		#logout {
			margin-bottom: 0;
		}

		.statusbar {
			width: var(--sidebar-width);
			margin-bottom: 0.4rem;
		}
		.statusbar li {
			margin-top: 0.2rem;
		}

		.statusbar-text-container {
			display: flex;
		}
		.statusbar-text {
			padding-right: 0.4rem;
			padding-left: 0.4rem;
			color: var(--color-text);
			font-size: 0.4em;
		}
		.statusbar-number {
			margin-left: auto;
		}
		.statusbar-progressbar {
			height: 0.3rem;
			margin-right: 0.3rem;
			margin-left: 0.3rem;
			padding: 0.05rem;
			background: var(--colorbg);
			border-radius: 0.2rem;
		}
		.statusbar-progressbar span {
			display: block;
			width: 50%;
			height: 100%;
			background: var(--color1);
			border-top-left-radius: 0.5rem;
			border-top-right-radius: 0.2rem;
			border-bottom-right-radius: 0.2rem;
			border-bottom-left-radius: 0.5rem;
		}
	</style>
	<ul class="statusbar">
		<li>
			<div class="statusbar-text-container">
				<span class="statusbar-text">CPU</span>
				<span class="statusbar-text statusbar-number"
					>{{ .status.CPUUsage }}%</span
				>
			</div>
			<div class="statusbar-progressbar">
				<span style="width: {{ .status.CPUUsage }}%"></span>
			</div>
		</li>
		<li>
			<div class="statusbar-text-container">
				<span class="statusbar-text">RAM</span>
				<span class="statusbar-text statusbar-number"
					>{{ .status.RAMUsage }}%</span
				>
			</div>
			<div class="statusbar-progressbar">
				<span style="width: {{ .status.RAMUsage }}%"></span>
			</div>
		</li>
		<li>
			<div class="statusbar-text-container">
				<span class="statusbar-text">DISK</span>
				<span
					style="margin: auto; font-size: 0.35rem"
					class="statusbar-text"
					>{{ .status.DiskUsageFormatted }}</span
				>
				<span class="statusbar-text statusbar-number"
					>{{ .status.DiskUsage }}%</span
				>
			</div>
			<div class="statusbar-progressbar">
				<span style="width: {{ .status.DiskUsage }}%"></span>
			</div>
		</li>
	</ul>`
