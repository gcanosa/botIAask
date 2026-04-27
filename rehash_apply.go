package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"botIAask/ai"
	"botIAask/config"
	"botIAask/irc"
	"botIAask/logger"
	"botIAask/rss"
	"botIAask/stats"
	"botIAask/web"
)

// rehashState holds dependencies for a full on-disk config reload.
type rehashState struct {
	configPath   string
	aiClient     *ai.Client
	bot          *irc.Bot
	rssFetcher   *rss.Fetcher
	statsTracker *stats.Tracker
	rssDB        *rss.Database
	webMu        *sync.Mutex
	webRef       **web.Server
	startWeb     func(cfg *config.Config)
}

func doApplyRehash(s *rehashState, source string, fromWeb bool) error {
	before, err := config.CloneConfig(s.bot.GetConfig())
	if err != nil {
		return err
	}
	newCfg, err := config.LoadConfig(s.configPath)
	if err != nil {
		return err
	}
	diff := config.RehashDiff(before, newCfg)
	t := time.Now().Format(time.RFC3339)

	s.aiClient.UpdateConfig(newCfg.AI.LMStudioURL, newCfg.AI.Model)
	s.bot.ApplyLiveConfig(newCfg)
	s.rssFetcher.ApplyConfig(newCfg)
	s.statsTracker.ApplyConfig(newCfg)
	if err := s.rssDB.RepairEmptySourceHackerNewsWhenSingleHNFeed(newCfg.RSS.FeedURLs); err != nil {
		log.Printf("RSS: repair source column after rehash: %v", err)
	}
	logger.SetRotationDays(newCfg.Logger.RotationDays)

	if err := s.applyWebHot(before, newCfg, fromWeb); err != nil {
		return err
	}

	s.bot.NotifyLoggedInAdminsRehashSummary(source, t, diff)
	log.Printf("Config rehash complete (%s)", source)
	return nil
}

func webSameListen(a, b config.WebConfig) bool {
	return a.Host == b.Host && a.Port == b.Port
}

func (s *rehashState) applyWebHot(before, newCfg *config.Config, fromWeb bool) error {
	s.webMu.Lock()
	ref := *s.webRef
	s.webMu.Unlock()

	if !newCfg.Web.Enabled {
		if ref == nil {
			return nil
		}
		ref.SetConfig(newCfg)
		doStop := func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			return ref.Stop(ctx)
		}
		if fromWeb {
			go func(r *web.Server) {
				if err := doStop(); err != nil {
					log.Printf("web: stop: %v", err)
				}
				s.webMu.Lock()
				*s.webRef = nil
				s.webMu.Unlock()
			}(ref)
			return nil
		}
		if err := doStop(); err != nil {
			return fmt.Errorf("web: stop: %w", err)
		}
		s.webMu.Lock()
		*s.webRef = nil
		s.webMu.Unlock()
		return nil
	}

	// newCfg.Web.Enabled
	s.webMu.Lock()
	ref = *s.webRef
	s.webMu.Unlock()

	if ref == nil {
		if s.startWeb == nil {
			return fmt.Errorf("web: hot enable not configured (startWeb nil)")
		}
		s.startWeb(newCfg)
		return nil
	}

	ref.SetConfig(newCfg)
	if webSameListen(before.Web, newCfg.Web) {
		return nil
	}
	if fromWeb {
		r := ref
		go func() {
			if err := runRebindFor(r, newCfg); err != nil {
				log.Printf("web: rebind: %v", err)
			}
		}()
		return nil
	}
	if err := runRebindFor(ref, newCfg); err != nil {
		return fmt.Errorf("web: rebind: %w", err)
	}
	return nil
}

// runRebindFor stops a running Server and starts it again (same object, new address from config).
func runRebindFor(srv *web.Server, newCfg *config.Config) error {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := srv.Stop(ctx); err != nil {
		return err
	}
	srv.SetConfig(newCfg)
	return srv.Start()
}
