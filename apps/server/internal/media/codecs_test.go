package media

import (
	"fmt"
	"testing"

	"github.com/pion/webrtc/v4"
)

func videoOffer(codec, fmtp string) string {
	return fmt.Sprintf("v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\nm=video 9 UDP/TLS/RTP/SAVPF 96\r\nc=IN IP4 127.0.0.1\r\na=recvonly\r\na=rtpmap:96 %s/90000\r\na=fmtp:96 %s\r\n", codec, fmtp)
}

func TestViewerCodecProfiles(t *testing.T) {
	for _, test := range []struct {
		name, source, sourceFmtp, receiver, receiverFmtp string
		compatible                                       bool
	}{
		{"legacy VP8", "VP8", "", "VP8", "", true},
		{"VP9 viewer cannot receive AV1", "AV1", "", "VP9", "profile-id=0", false},
		{"VP9 default profile", "VP9", "profile-id=0", "VP9", "", true},
		{"VP9 incompatible profile", "VP9", "profile-id=0", "VP9", "profile-id=2", false},
		{"AV1 compatible", "AV1", "", "AV1", "profile=0", true},
		{"AV1 incompatible", "AV1", "", "AV1", "profile=1", false},
		{"H264 compatible higher level", "H264", "packetization-mode=1;profile-level-id=42e01f", "H264", "packetization-mode=1;profile-level-id=42e034", true},
		{"H264 incompatible profile", "H264", "packetization-mode=1;profile-level-id=42e01f", "H264", "packetization-mode=1;profile-level-id=640034", false},
		{"H264 insufficient level", "H264", "packetization-mode=1;profile-level-id=42e02a", "H264", "packetization-mode=1;profile-level-id=42e01f", false},
		{"H264 incompatible packetization", "H264", "packetization-mode=1;profile-level-id=42e01f", "H264", "packetization-mode=0;profile-level-id=42e01f", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			codec := webrtc.RTPCodecCapability{MimeType: "video/" + test.source, ClockRate: 90000, SDPFmtpLine: test.sourceFmtp}
			if got := offerSupportsCodec(videoOffer(test.receiver, test.receiverFmtp), codec); got != test.compatible {
				t.Fatalf("offer compatibility=%v want=%v", got, test.compatible)
			}
		})
	}
}

func TestViewerRejectsDisabledMediaAndUnlistedPayload(t *testing.T) {
	codec := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}
	for _, offer := range []string{
		"not SDP",
		"v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\nm=video 0 UDP/TLS/RTP/SAVPF 96\r\na=rtpmap:96 VP8/90000\r\n",
		"v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\nm=video 9 UDP/TLS/RTP/SAVPF 98\r\na=rtpmap:96 VP8/90000\r\na=rtpmap:98 AV1/90000\r\n",
	} {
		if offerSupportsCodec(offer, codec) {
			t.Fatal("accepted codec which cannot be negotiated in the media section")
		}
	}
}
