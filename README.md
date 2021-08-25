# RTMP Ingest
[![Go Report Card](https://goreportcard.com/badge/github.com/clone1018/rtmp-ingest)](https://goreportcard.com/report/github.com/clone1018/rtmp-ingest)

Experimental RTMP ingest for Glimesh.tv

Converts RTMP input into RTP packets, and then works with the Orchestrator to get them where they need to be.

**Using stunnel is required since Go does not support PSK based TLS auth.**

## Building
```shell
go build

export RMTP_INGEST_GLIMESH_ADDRESS=https://glimesh.tv
export RMTP_INGEST_GLIMESH_CLIENT_ID=some_client_id
export RMTP_INGEST_GLIMESH_CLIENT_SECRET=some_client_secret
# Assumes a stunnel connection setup with proper PSK to orchestrator
export RTMP_INGEST_ORCHESTRATOR_ADDRESS=localhost:18085
./rtmp-ingest
```

## Helpful GStreamer Commands
**Moving Circle**
```shell
export RTMP_URL=rtmp://localhost
gst-launch-1.0 videotestsrc pattern=ball flip=true animation-mode=running-time motion=sweep is-live=true ! timeoverlay ! video/x-raw,format=I420,width=1280,height=720,framerate=60/1 ! x264enc speed-preset=ultrafast tune=zerolatency key-int-max=20 ! flvmux name=flvmux ! rtmpsink location=$RTMP_URL audiotestsrc ! alawenc ! flvmux.
```

**Clock**
```shell
gst-launch-1.0 videotestsrc is-live=true ! timeoverlay ! video/x-raw,format=I420,width=1280,height=720,framerate=60/1 ! x264enc speed-preset=ultrafast tune=zerolatency key-int-max=20 ! flvmux name=flvmux ! rtmpsink location=$RTMP_URL audiotestsrc ! alawenc ! flvmux.
```
