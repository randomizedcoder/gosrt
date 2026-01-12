#!/etc/profiles/per-user/das/bin/bash
ffmpeg \
  -re \
  -f lavfi -i "testsrc2=size=1920x1080:rate=60" \
  -vaapi_device /dev/dri/renderD128 \
  -vf 'format=nv12,hwupload' \
  -c:v h264_vaapi \
  -b:v 20M \
  -maxrate 20M \
  -bufsize 20M \
  -g 120 \
  -f mpegts \
  -transtype live \
  "srt://172.16.40.46:6001?conntimeo=3000&peerlatency=3000&rcvlatency=3000&peeridletimeo=90000&streamid=publish:/live/stream"

#  "srt://172.16.40.46:6001?streamid=publish:/live/stream"
