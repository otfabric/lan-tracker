package daemon

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/otfabric/lan-tracker/internal/collect/capture"
	"github.com/otfabric/lan-tracker/internal/collect/capture/decoders"
	"github.com/otfabric/lan-tracker/internal/collect/neighbor"
	"github.com/otfabric/lan-tracker/internal/collect/replay"
	"github.com/otfabric/lan-tracker/internal/config"
	"github.com/otfabric/lan-tracker/internal/correlate"
	"github.com/otfabric/lan-tracker/internal/events"
	"github.com/otfabric/lan-tracker/internal/identify"
	"github.com/otfabric/lan-tracker/internal/iface"
	"github.com/otfabric/lan-tracker/internal/observation"
	"github.com/otfabric/lan-tracker/internal/platform"
)

// drainTimeout bounds how long shutdown waits for the correlator to process
// what is already on the bus.
const drainTimeout = 10 * time.Second

func openReplay(cfg *config.Config) (*replay.Player, error) {
	var sources []replay.Source
	speed := 0.0
	for _, ic := range cfg.Interfaces {
		sources = append(sources, replay.Source{Interface: ic.Name, File: ic.Replay.File, Prefixes: ic.Prefixes})
		speed = ic.Replay.Speed
	}
	return replay.Open(replay.Options{Sources: sources, Speed: speed, Protocols: decoders.Protocols(cfg.Passive.Protocols)})
}

// startPipeline starts the correlator and the collectors. d.stop shuts them
// down in order: collectors first, then the correlator once it has drained
// the bus.
func (d *Daemon) startPipeline(ctx context.Context) error {
	cfg := d.cfg.Load()
	vendors, err := identify.Load(cfg.Identity.OUIOverride)
	if err != nil {
		return err
	}
	d.log.Info("vendor table loaded", "assignments", vendors.Len())
	d.bus = observation.NewBus(observation.DefaultBusSize)
	// Events record the kernel clock's sync state; a replay's events carry
	// recorded times, so their state is unknown.
	var clockState func() platform.ClockState
	if !cfg.ReplayMode() && d.backends.Clock != nil {
		clockState = d.backends.Clock.State
		if st := clockState(); st != platform.ClockSynced {
			d.log.Warn("system clock is not known to be synchronised; events are marked until it is", "clock", st)
		}
	}
	d.events = events.NewEngine(d.store, d.log, clockState)
	d.correlator, err = correlate.New(ctx, correlate.Options{
		Store: d.store, Events: d.events, Vendors: vendors, Clock: d.clock, Logger: d.log, Config: cfg,
		DataDriven: cfg.ReplayMode(), Recovery: d.store.Recovery(),
	})
	if err != nil {
		return fmt.Errorf("start correlator: %w", err)
	}

	// The correlator outlives ctx so it can drain the bus during shutdown.
	corrCtx, corrCancel := context.WithCancel(context.WithoutCancel(ctx))
	corrDone := make(chan struct{})
	go func() {
		defer close(corrDone)
		_ = d.correlator.Run(corrCtx, d.bus)
	}()

	collCtx, collCancel := context.WithCancel(ctx)
	if !cfg.ReplayMode() {
		// Passive collectors leaving and regaining running are collection
		// gaps the correlator records (OBSERVATION_GAP).
		d.registry.Watch(func(s platform.CollectorStatus) {
			if s.Collector != platform.CollectorCapture && s.Collector != platform.CollectorNeighbor || collCtx.Err() != nil {
				return
			}
			_ = d.bus.PublishCollector(collCtx, observation.CollectorState{Time: s.Since, Interface: s.Interface,
				Collector: s.Collector, State: string(s.State), Error: s.Error})
		})
	}
	var wg sync.WaitGroup
	start := func(name string, run func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := run(collCtx); err != nil {
				d.log.Error("collector stopped", "collector", name, "err", err)
			}
		}()
	}

	if d.player != nil {
		d.player.SetRegistry(d.registry, d.log)
		start(platform.CollectorReplay, func(ctx context.Context) error {
			if err := d.player.Run(ctx, d.bus); err != nil {
				return err
			}
			if ctx.Err() == nil {
				// The writer's flush ticker runs on the simulated clock,
				// which stops with the replay: commit what is queued so the
				// API and --offline see all of it.
				if err := d.store.Flush(ctx); err != nil {
					return err
				}
				d.log.Info("replay data committed")
				if cfg.Replay.ExitWhenDone {
					d.replayDone <- struct{}{}
				}
			}
			return nil
		})
	} else {
		var live []string
		for _, ic := range cfg.Interfaces {
			live = append(live, ic.Name)
		}
		d.ifaces = iface.New(iface.Options{Monitor: d.backends.Interfaces, Bus: d.bus, Clock: d.clock, Interfaces: live,
			Registry: d.registry, Logger: d.log})
		start(platform.CollectorInterface, d.ifaces.Run)
		nb := neighbor.New(neighbor.Options{Source: d.backends.Neighbors, Bus: d.bus, Clock: d.clock, Interfaces: live,
			Resync: cfg.Neighbor.ResyncInterval.D(), Registry: d.registry, Logger: d.log})
		start(platform.CollectorNeighbor, nb.Run)
		var captured []capture.Interface
		for _, ic := range cfg.Interfaces {
			if ic.PassiveEnabled() {
				captured = append(captured, capture.Interface{Name: ic.Name, Promiscuous: ic.Passive.Promiscuous})
			} else {
				d.registry.Set(ic.Name, platform.CollectorCapture, d.backends.Capturer.Backend(), platform.StateDisabled, nil)
			}
		}
		if len(captured) > 0 {
			d.capture = capture.New(capture.Options{Capturer: d.backends.Capturer, Bus: d.bus, Clock: d.clock,
				Interfaces: captured, Protocols: decoders.Protocols(cfg.Passive.Protocols), RingSize: int(cfg.Passive.RingSize),
				Registry: d.registry, Logger: d.log})
			start(platform.CollectorCapture, d.capture.Run)
		}
		if err := d.startScheduler(ctx, start); err != nil {
			collCancel()
			wg.Wait()
			corrCancel()
			<-corrDone
			return err
		}
	}

	d.stop = func() {
		collCancel()
		wg.Wait()
		dctx, cancel := context.WithTimeout(context.Background(), drainTimeout)
		if err := d.bus.Barrier(dctx); err != nil {
			d.log.Warn("correlator did not drain the bus before shutdown", "err", err)
		}
		cancel()
		corrCancel()
		<-corrDone
		d.bus.Close() // a late operator request fails instead of waiting for the correlator
		if n := d.bus.Dropped(); n > 0 {
			d.log.Warn("observations dropped because the bus was full", "dropped", n)
		}
	}
	return nil
}
