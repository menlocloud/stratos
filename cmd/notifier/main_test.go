package main

import "testing"

func TestSplitCSV(t *testing.T) {
	got := splitCSV(" nova, neutron ,,cinder ")
	want := []string{"nova", "neutron", "cinder"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	if splitCSV("  ") != nil {
		t.Errorf("blank should be nil")
	}
}

func TestLoadConfig_RequiresTargetAndBroker(t *testing.T) {
	// A minimally valid config (URL + target + secret) with defaults filled in.
	t.Setenv("RABBITMQ_URL", "amqp://u:p@host:5672/")
	t.Setenv("TARGET_URL", "https://cloud/api/v1/notifications/svc/RegionOne")
	t.Setenv("TARGET_SECRET", "s3cret")
	c, err := loadConfig()
	if err != nil {
		t.Fatalf("valid config errored: %v", err)
	}
	if c.queue != "stratos-notifier" || c.bindingKey != "notifications.#" || c.prefetch != 20 {
		t.Errorf("defaults wrong: %+v", c)
	}
	if len(c.exchanges) != 8 {
		t.Errorf("default exchanges = %v", c.exchanges)
	}

	// Missing TARGET_SECRET → error.
	t.Setenv("TARGET_SECRET", "")
	if _, err := loadConfig(); err == nil {
		t.Errorf("missing TARGET_SECRET should error")
	}

	// Neither URL nor ADDRESSES → error.
	t.Setenv("TARGET_SECRET", "s3cret")
	t.Setenv("RABBITMQ_URL", "")
	if _, err := loadConfig(); err == nil {
		t.Errorf("missing broker should error")
	}
}
