package signer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mishmar-io/mishmar/venues/hyperliquid"
)

// The signer reads Hyperliquid's list of perp markets from Hyperliquid
// itself and takes a coin's asset index and size decimals from there. It
// does not ask the engine for them: which market an order is on is decided
// by the agent's proposal and the venue's list, and by nothing else.
//
// One read covers every coin on a network. What the signer signs depends on
// how old its last answer from the venue is:
//
//   - read within HLListFresh: orders and cancels are checked and signed;
//   - older, because a new read failed: only reduce-only orders and cancels
//     are signed, checked against that last answer;
//   - never read since the signer started: nothing is signed on Hyperliquid.
//
// The rule is applied where a request is checked (hlAgeRule, from
// checkHyperliquid and prepareHyperliquid), not where the list is fetched.

const (
	// HLListFresh is how long Hyperliquid's list of markets is used without
	// asking again.
	HLListFresh = 10 * time.Minute
	// hlListTimeout bounds one read of the list.
	hlListTimeout = 5 * time.Second
	// hlListMaxBytes caps the answer read; the list is well under a megabyte.
	hlListMaxBytes = 4 << 20
	// hlListRetry is how long the signer waits after a failed read before
	// it tries again, so a machine the venue is rate-limiting asks less.
	hlListRetry = 10 * time.Second
)

// HLMarket is a coin as Hyperliquid last listed it to this signer, and when.
type HLMarket struct {
	hyperliquid.Market
	// ReadAt is when the list this came from was read from Hyperliquid.
	ReadAt time.Time
}

// ErrHLListUnread is Policy.Market's answer when the signer has not read
// the network's list since it started.
var ErrHLListUnread = errors.New("this signer has not been able to read Hyperliquid's market list since it started; nothing is signed on Hyperliquid until it can")

// hlLists is the signer's copy of each network's list.
type hlLists struct {
	// fetch lets one read run at a time; the others wait for its result.
	fetch sync.Mutex
	mu    sync.Mutex
	lists map[hyperliquid.Network]hlList
}

type hlList struct {
	markets hyperliquid.Markets
	readAt  time.Time
	// triedAt and tryErr are the last failed read since readAt.
	triedAt time.Time
	tryErr  error
}

func (l *hlLists) get(network hyperliquid.Network) hlList {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lists[network]
}

func (l *hlLists) set(network hyperliquid.Network, list hlList) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lists == nil {
		l.lists = map[hyperliquid.Network]hlList{}
	}
	l.lists[network] = list
}

// hlHTTP reads the list: certificates are checked as Go does by default,
// and a redirect is not followed, so the answer comes from the host asked.
var hlHTTP = &http.Client{
	Timeout:       hlListTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// refreshHLMarkets reads the network's list from Hyperliquid when the
// signer's copy is older than HLListFresh. A failed read leaves the copy as
// it was and is not tried again for hlListRetry.
func (s *Signer) refreshHLMarkets(ctx context.Context, network hyperliquid.Network) error {
	s.hl.fetch.Lock()
	defer s.hl.fetch.Unlock()
	now := s.now()
	list := s.hl.get(network)
	if !list.readAt.IsZero() && now.Sub(list.readAt) < HLListFresh {
		return nil
	}
	if list.tryErr != nil && now.Sub(list.triedAt) < hlListRetry {
		return list.tryErr
	}
	url := network.InfoURL()
	if s.HyperliquidInfoURL != nil {
		url = s.HyperliquidInfoURL(network)
	}
	markets, err := readHLMarkets(ctx, url)
	if err != nil {
		list.triedAt, list.tryErr = now, err
	} else {
		list = hlList{markets: markets, readAt: now}
	}
	s.hl.set(network, list)
	return err
}

// readHLMarkets asks one info endpoint for the list of perp markets.
func readHLMarkets(ctx context.Context, url string) (hyperliquid.Markets, error) {
	ctx, cancel := context.WithTimeout(ctx, hlListTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(hyperliquid.MarketsRequest))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hlHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reading Hyperliquid's market list: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("reading Hyperliquid's market list: the venue answered %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, hlListMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading Hyperliquid's market list: %w", err)
	}
	if len(body) > hlListMaxBytes {
		return nil, fmt.Errorf("reading Hyperliquid's market list: the answer is larger than %d bytes", hlListMaxBytes)
	}
	return hyperliquid.ParseMarkets(body)
}

// hlMarket is what Policy.Market reads: the coin in the signer's copy of
// the network's list, whatever its age, with when that list was read.
func (s *Signer) hlMarket(network hyperliquid.Network, symbol string) (HLMarket, error) {
	list := s.hl.get(network)
	if list.readAt.IsZero() {
		return HLMarket{}, ErrHLListUnread
	}
	m, err := list.markets.Find(symbol)
	if err != nil {
		return HLMarket{}, err
	}
	return HLMarket{Market: m, ReadAt: list.readAt}, nil
}

// hlMarketRefusal is the refusal for a coin Policy.Market could not give.
func hlMarketRefusal(err error) *Refusal {
	if errors.Is(err, ErrHLListUnread) {
		return &Refusal{RefuseVenueList, err.Error()}
	}
	return &Refusal{RefuseMalformed, trimVenue(err)}
}

// hlAgeRule is what the age of the venue's list allows. closing is a cancel
// or a reduce-only order, read from the action being signed: those are
// signed against the venue's last answer however old it is. Anything else
// needs a list read within HLListFresh and a coin the venue has not
// delisted.
func hlAgeRule(m HLMarket, closing bool, now time.Time) *Refusal {
	switch {
	case closing:
		return nil
	case m.Delisted:
		return &Refusal{RefuseMalformed, fmt.Sprintf("%s is delisted on Hyperliquid; only reduce-only orders and cancels are signed for it", m.Name)}
	case now.Sub(m.ReadAt) >= HLListFresh:
		return &Refusal{RefuseVenueList, fmt.Sprintf("Hyperliquid's market list could not be read from this machine since %s; until it can, only reduce-only orders and cancels are signed", m.ReadAt.UTC().Format("15:04 UTC on 2 January"))}
	}
	return nil
}
