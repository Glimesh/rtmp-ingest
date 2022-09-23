package main

import (
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Glimesh/rtmp-ingest/pkg/protocols/ftl"

	"github.com/Glimesh/rtmp-ingest/pkg/orchestrator"
	"github.com/Glimesh/rtmp-ingest/pkg/services/glimesh"

	"net/http"
	_ "net/http/pprof"

	"github.com/evalphobia/logrus_sentry"
	"github.com/getsentry/sentry-go"
	"github.com/sirupsen/logrus"
)

func main() {
	log := logrus.New()
	log.Level = logrus.DebugLevel
	// log.SetReportCaller(true)
	log.SetFormatter(&logrus.TextFormatter{
		ForceColors: true,
	})

	var streamManager StreamManager

	hostname, err := os.Hostname()
	if err != nil {
		// How tf
		log.Fatal(err)
	}

	sentryDsn, setupSentry := os.LookupEnv("SENTRY_DSN")
	if setupSentry {
		err := sentry.Init(sentry.ClientOptions{
			Dsn: sentryDsn,
		})
		if err != nil {
			log.Fatalf("sentry.Init: %s", err)
		}
		hook, err := logrus_sentry.NewSentryHook(sentryDsn, []logrus.Level{
			logrus.PanicLevel,
			logrus.FatalLevel,
			logrus.ErrorLevel,
		})

		if err == nil {
			log.Hooks.Add(hook)
		}
	}

	_, debugVideo := os.LookupEnv("RTMP_INGEST_DEBUG_VIDEO")

	// Should use viper or something in the future
	glimeshService := glimesh.New(glimesh.Config{
		Address:      os.Getenv("RTMP_INGEST_GLIMESH_ADDRESS"),
		ClientID:     os.Getenv("RTMP_INGEST_GLIMESH_CLIENT_ID"),
		ClientSecret: os.Getenv("RTMP_INGEST_GLIMESH_CLIENT_SECRET"),
	})
	err = glimeshService.Connect()
	if err != nil {
		log.Fatal(err)
	}

	orchTransport, err := net.Dial("tcp", os.Getenv("RTMP_INGEST_ORCHESTRATOR_ADDRESS"))
	if err != nil {
		log.Fatal(err)
	}
	orch := orchestrator.NewClient(orchestrator.Config{
		RegionCode: "global",
		Hostname:   hostname,
		Logger:     log.WithFields(logrus.Fields{"app": "orchestrator"}),
		Callbacks: orchestrator.Callbacks{
			OnStreamRelaying: func(message orchestrator.StreamRelayingMessage) {
				if message.Context == 1 {
					go func() {
						retries := 0
						for {
							log.Infof("Starting relay for %d to %s", message.ChannelID, message.TargetHostname)
							// This is a blocking call, once if returns we should conditionally stop the relay
							err := streamManager.RelayMedia(message.ChannelID, message.TargetHostname, ftl.DefaultPort, message.StreamKey)
							if err == nil {
								// Good ending
								log.Infof("Ending relay for %d to %s", message.ChannelID, message.TargetHostname)
								return

							}

							// For some reason the relay media command errored, we need to loop & retry
							retries++
							log.Error(err)
							if retries > 5 {
								log.Errorf("Relay for %d to %s failed 5 times, cancelling", message.ChannelID, message.TargetHostname)
								return
							}
							time.Sleep(1 * time.Second)
						}
					}()
				} else {
					log.Infof("Removing relay for %d to %s per orchestrator", message.ChannelID, message.TargetHostname)
					err := streamManager.StopRelay(message.ChannelID, message.TargetHostname)
					if err != nil {
						log.Error(err)
					}
				}
			},
		},
	})
	if err := orch.Connect(orchTransport); err != nil {
		log.Fatal(err)
	}

	streamManager = NewStreamManager(orch, glimeshService)

	closeHandler(orch, streamManager)

	go func() {
		log.Println(http.ListenAndServe("localhost:6060", nil))
	}()

	// Blocking call to start the RTMP server
	NewRTMPServer(streamManager, log.WithFields(logrus.Fields{"app": "rtmp"}), debugVideo)
}

func closeHandler(orch orchestrator.Client, streamManager StreamManager) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		<-c

		// Stop all streams we're handling
		for k := range streamManager.streams {
			streamManager.StopStream(k)
		}

		// Tell orchestrator goodbye for now
		orch.Close()

		os.Exit(0)
	}()
}
