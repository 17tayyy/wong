package remote

import "context"

// Holder acts on the brain's keyring with the key its client holds: it approves machines and lets the cloud AIs in,
// for a dashboard that runs where the key is, on a machine's copy or in a browser.
type Holder struct{ C *Client }

func (h Holder) Keys(ctx context.Context) (Keys, error) { return h.C.Keys(ctx) }

func (h Holder) Self() []byte { return h.C.Device.Public() }

func (h Holder) Approve(ctx context.Context, req KeyRequest) error {
	if err := h.C.Unlock(ctx); err != nil {
		return err
	}
	return h.C.ApproveKey(ctx, req, h.C.Key)
}

func (h Holder) LetCloudAIs(ctx context.Context) error {
	if err := h.C.Unlock(ctx); err != nil {
		return err
	}
	return h.C.LetCloudAIs(ctx, h.C.Key)
}

func (h Holder) StopCloudAIs(ctx context.Context) error { return h.C.StopCloudAIs(ctx) }
