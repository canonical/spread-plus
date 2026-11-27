package spread

import (
	"testing"
	"time"
)

func TestStopCancelsContentPreparation(t *testing.T) {
	r := &Runner{}
	contentStarted := make(chan struct{})
	r.contentTomb.Go(func() error {
		close(contentStarted)
		<-r.contentTomb.Dying()
		return nil
	})
	<-contentStarted

	r.tomb.Go(func() error {
		return r.contentTomb.Wait()
	})
	stopped := make(chan error, 1)
	go func() {
		stopped <- r.Stop()
	}()

	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("runner stop failed: %v", err)
		}
	case <-time.After(time.Second):
		r.contentTomb.Kill(nil)
		t.Fatal("runner stop did not cancel content preparation")
	}
}
