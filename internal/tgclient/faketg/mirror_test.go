package faketg

import (
	"reflect"
	"sort"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func signatureWithoutReceiver(fn reflect.Type, hasReceiver bool) string {
	start := 0
	if hasReceiver {
		start = 1
	}
	out := "("
	for i := start; i < fn.NumIn(); i++ {
		out += fn.In(i).String() + ","
	}
	out += ")("
	for i := 0; i < fn.NumOut(); i++ {
		out += fn.Out(i).String() + ","
	}
	return out + ")"
}

func TestFakeMirrorsTgclient(t *testing.T) {
	concrete := reflect.TypeFor[*Client]()
	interfaces := []reflect.Type{
		reflect.TypeFor[tgclient.Sender](),
		reflect.TypeFor[tgclient.RangeReader](),
		reflect.TypeFor[tgclient.ChannelRPC](),
		reflect.TypeFor[tgclient.AuthRPC](),
	}
	var mirrored []string
	for _, iface := range interfaces {
		t.Run(iface.Name(), func(t *testing.T) {
			if !concrete.Implements(iface) {
				t.Fatalf("*Client does not implement %s", iface)
			}
			for i := 0; i < iface.NumMethod(); i++ {
				want := iface.Method(i)
				got, ok := concrete.MethodByName(want.Name)
				if !ok {
					t.Fatalf("*Client has no method %s", want.Name)
				}
				if a, b := signatureWithoutReceiver(got.Type, true), signatureWithoutReceiver(want.Type, false); a != b {
					t.Fatalf("%s signature = %s, want %s", want.Name, a, b)
				}
				mirrored = append(mirrored, want.Name)
			}
		})
	}
	sort.Strings(mirrored)

	scriptable := []Method{
		MethodSendFileIdempotent, MethodSendControlIdempotent, MethodResolveDocument, MethodResolveDocuments,
		MethodReadDocumentRange, MethodCreateChannel, MethodGetHistory, MethodGetChannelDifference,
		MethodGetChannelPts, MethodGetMessages, MethodDeleteMessages, MethodListOwnedChannels,
		MethodResolveChannel, MethodSendToSavedMessages, MethodSelf, MethodSendCode, MethodSignIn,
		MethodCheckPassword, MethodGetPasswordHint, MethodLogOut,
	}
	names := make([]string, len(scriptable))
	for i, m := range scriptable {
		names[i] = string(m)
		if !knownMethod(m) {
			t.Fatalf("method %s is not scriptable", m)
		}
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, mirrored) {
		t.Fatalf("scriptable methods %v differ from mirrored interface methods %v", names, mirrored)
	}
}
