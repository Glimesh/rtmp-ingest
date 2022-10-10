# RTMP Ingest
[![Go Report Card](https://goreportcard.com/badge/github.com/Glimesh/rtmp-ingest)](https://goreportcard.com/report/github.com/Glimesh/rtmp-ingest)

Experimental RTMP ingest for Glimesh.tv

Converts RTMP input into RTP packets, and then works with the Orchestrator to get them where they need to be.

**Using stunnel is required since Go does not support PSK based TLS auth.**

## General Usage
```shell
go build

export RTMP_INGEST_GLIMESH_ADDRESS=https://glimesh.tv
export RTMP_INGEST_GLIMESH_CLIENT_ID=some_client_id
export RTMP_INGEST_GLIMESH_CLIENT_SECRET=some_client_secret
# Assumes a stunnel connection setup with proper PSK to orchestrator
export RTMP_INGEST_ORCHESTRATOR_ADDRESS=localhost:18085
./rtmp-ingest
```

### macOS Development
```
brew install opusfile fdk-aac
```

### Ubuntu / Linux Development
```
apt install -y pkg-config build-essential libopusfile-dev libfdk-aac-dev libavutil-dev libavcodec-dev libswscale-dev
```

## Helpful FFmpeg Commands
```
ffmpeg -re -f lavfi \
    -i "testsrc=size=1280x720:rate=60[out0];sine=frequency=1000:sample_rate=48000[out1]" \
    -vf "[in]drawtext=fontsize=96: box=1: boxcolor=black@0.75: boxborderw=5: fontcolor=white: x=(w-text_w)/2: y=((h-text_h)/2)+((h-text_h)/-2): text='Hello from FFmpeg', drawtext=fontsize=96: box=1: boxcolor=black@0.75: boxborderw=5: fontcolor=white: x=(w-text_w)/2: y=((h-text_h)/2)+((h-text_h)/2): text='%{gmtime\:%H\\\\\:%M\\\\\:%S} UTC'[out]" \
    -nal-hrd cbr \
    -metadata:s:v encoder=test \
    -vcodec libx264 \
    -acodec aac \
    -preset veryfast \
    -profile:v baseline \
    -tune zerolatency \
    -bf 0 \
    -g 0 \
    -b:v 6320k \
    -b:a 160k \
    -ac 2 \
    -ar 48000 \
    -minrate 6320k \
    -maxrate 6320k \
    -bufsize 6320k \
    -muxrate 6320k \
    -r 60 \
    -pix_fmt yuv420p \
    -color_range 1 -colorspace 1 -color_primaries 1 -color_trc 1 \
    -flags:v +global_header \
    -bsf:v dump_extra \
    -x264-params "nal-hrd=cbr:min-keyint=2:keyint=2:scenecut=0:bframes=0" \
    -f flv "$RTMP_URL"
```

## Helpful GStreamer Commands
**Moving Circle**
```shell
export RTMP_URL=rtmp://localhost/live/stream-key
gst-launch-1.0 videotestsrc pattern=ball flip=true animation-mode=running-time motion=sweep is-live=true ! timeoverlay ! video/x-raw,format=I420,width=1280,height=720,framerate=60/1 ! x264enc speed-preset=ultrafast tune=zerolatency key-int-max=20 ! flvmux name=flvmux ! rtmpsink location=$RTMP_URL audiotestsrc ! alawenc ! flvmux.
```

**Clock**
```shell
gst-launch-1.0 videotestsrc is-live=true ! timeoverlay ! video/x-raw,format=I420,width=1280,height=720,framerate=60/1 ! x264enc speed-preset=ultrafast tune=zerolatency key-int-max=20 ! flvmux name=flvmux ! rtmpsink location=$RTMP_URL audiotestsrc ! alawenc ! flvmux.
```
