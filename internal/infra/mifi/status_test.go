package mifi_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/1995parham-me/sms2telegram/internal/infra/mifi"
)

// Captured from the DWR-910M on 2026-11-01, stray space after </sn> and all.
const dashboard = `x("<deviceinfo><batteryPercent>67%</batteryPercent><functionTimes>05:22:01</functionTimes>` +
	`<hotcount>0</hotcount><imsi></imsi><issim>true</issim><networkType>Irancell  4G</networkType>` +
	`<strengthDbm>-97dBm</strengthDbm><strengthLevel>5</strengthLevel><sn>AX3DMM3008326</sn> ` +
	`<dashSinr>4</dashSinr><dashBand>7 </dashBand></deviceinfo>")`

const irancell = "Irancell  4G"

func TestStatusParsesDashboard(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/jsonp_dashboard" {
			http.NotFound(w, r)

			return
		}

		_, _ = w.Write([]byte(dashboard))
	}))
	defer srv.Close()

	c, err := mifi.New(srv.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	st, err := c.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	want := mifi.Status{SIM: true, Network: irancell, SignalDBm: -97, Uptime: 5*time.Hour + 22*time.Minute + time.Second}
	if st != want {
		t.Fatalf("got %+v, want %+v", st, want)
	}

	if !st.Registered() {
		t.Fatal("a SIM on Irancell 4G is registered")
	}
}

func TestRegistered(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		st   mifi.Status
		want bool
	}{
		{mifi.Status{SIM: true, Network: irancell, SignalDBm: 0, Uptime: 0}, true},
		{mifi.Status{SIM: false, Network: irancell, SignalDBm: 0, Uptime: 0}, false},
		{mifi.Status{SIM: true, Network: " ", SignalDBm: 0, Uptime: 0}, false},
		{mifi.Status{SIM: true, Network: "No Service", SignalDBm: 0, Uptime: 0}, false},
		{mifi.Status{SIM: true, Network: "Limited Service", SignalDBm: 0, Uptime: 0}, false},
	} {
		if got := tc.st.Registered(); got != tc.want {
			t.Errorf("%+v: got %v, want %v", tc.st, got, tc.want)
		}
	}
}
