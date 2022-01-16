package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// Represents the different types of transaction work that can be performed.
const (
	twAdd = "add"
)

// tranWork is signaled to the worker goroutine to perform transaction work.
type tranWork struct {
	action string
	txs    []Tx
}

// =============================================================================

// peerStatus represents information about the status
// of any given peer.
type peerStatus struct {
	Hash              string              `json:"hash"`
	LatestBlockNumber uint64              `json:"latest_block_number"`
	KnownPeers        map[string]struct{} `json:"known_peers"`
}

// =============================================================================

// bcWorker manages a goroutine that executes a write block
// call on a timer.
type bcWorker struct {
	node         *Node
	wg           sync.WaitGroup
	ticker       time.Ticker
	shut         chan struct{}
	startMining  chan bool
	cancelMining chan bool
	evHandler    EventHandler
}

// newBCWorker creates a blockWriter for writing transactions
// from the mempool to a new block.
func newBCWorker(node *Node, evHandler EventHandler) *bcWorker {
	ev := func(v string) {
		if evHandler != nil {
			evHandler(v)
		}
	}

	bw := bcWorker{
		node:         node,
		ticker:       *time.NewTicker(10 * time.Second),
		shut:         make(chan struct{}),
		startMining:  make(chan bool, 1),
		cancelMining: make(chan bool, 1),
		evHandler:    ev,
	}

	// Let's update the peer list and blocks.
	bw.runPeerOperation()

	// Add the two G's we are about to create.
	g := 2
	bw.wg.Add(g)
	started := make(chan bool)

	// This G handles finding new peers.
	go func() {
		bw.evHandler("bcWorker: runPeerOperation: mainG: G started")
		started <- true
		defer func() {
			bw.evHandler("bcWorker: runPeerOperation: mainG: G completed")
			bw.wg.Done()
		}()
	down:
		for {
			select {
			case <-bw.ticker.C:
				bw.runPeerOperation()
			case <-bw.shut:
				bw.evHandler("bcWorker: runPeerOperation: mainG: received shut signal")
				break down
			}
		}
	}()

	// This G handles mining.
	go func() {
		bw.evHandler("bcWorker: runMiningOperation: mainG: G started")
		started <- true
		defer func() {
			bw.evHandler("bcWorker: runMiningOperation: mainG: G completed")
			bw.wg.Done()
		}()
	down:
		for {
			select {
			case <-bw.startMining:
				bw.runMiningOperation()
			case <-bw.shut:
				bw.evHandler("bcWorker: runMiningOperation: mainG: received shut signal")
				break down
			}
		}
	}()

	// Wait for the two G's to report they are running.
	for i := 0; i < g; i++ {
		<-started
	}

	return &bw
}

// shutdown terminates the goroutine performing work.
func (bw *bcWorker) shutdown() {
	bw.evHandler("bcWorker: shutdown: started")
	defer bw.evHandler("bcWorker: shutdown: completed")

	bw.evHandler("bcWorker: shutdown: stop ticker")
	bw.ticker.Stop()

	bw.evHandler("bcWorker: shutdown: signal cancel mining")
	bw.signalCancelMining()

	bw.evHandler("bcWorker: shutdown: terminate goroutines")
	close(bw.shut)
	bw.wg.Wait()
}

// =============================================================================

// signalStartMining starts a mining operation.
func (bw *bcWorker) signalStartMining() {
	select {
	case bw.startMining <- true:
	default:
	}
	bw.evHandler("bcWorker: signalStartMining: mining signaled")
}

// signalCancelMining cancels a mining operation.
func (bw *bcWorker) signalCancelMining() {
	select {
	case bw.cancelMining <- true:
	default:
	}
	bw.evHandler("bcWorker: signalCancelMining: cancel signaled")
}

// =============================================================================

// runMiningOperation takes all the transactions from the mempool and writes a
// new block to the database.
func (bw *bcWorker) runMiningOperation() {
	bw.evHandler("bcWorker: runMiningOperation: mainG: mining started")
	defer bw.evHandler("bcWorker: runMiningOperation: mainG: mining completed")

	// Drain the cancel mining channel before starting.
	select {
	case <-bw.cancelMining:
		bw.evHandler("bcWorker: runMiningOperation: mainG: drained cancel channel")
	default:
	}

	// Make sure there are at least 2 transactions in the mempool.
	length := bw.node.QueryMempoolLength()
	if length < 2 {
		bw.evHandler(fmt.Sprintf("bcWorker: runMiningOperation: mainG: not enough transactions to mine: %d", length))
		return
	}

	// Create a context so mining can be cancelled.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Can't return from this function until these G's are complete.
	var wg sync.WaitGroup
	wg.Add(2)

	// This G exists to cancel the mining operation.
	go func() {
		defer func() { cancel(); wg.Done() }()

		select {
		case <-bw.cancelMining:
			bw.evHandler("bcWorker: runMiningOperation: cancelG: cancel mining")
		case <-ctx.Done():
			bw.evHandler("bcWorker: runMiningOperation: cancelG: context cancelled")
		}
	}()

	// This G is performing the mining.
	go func() {
		bw.evHandler("bcWorker: runMiningOperation: miningG: MineNewBlock: started")
		defer func() {
			bw.evHandler("bcWorker: runMiningOperation: miningG: MineNewBlock: completed")
			cancel()
			wg.Done()
		}()

		block, err := bw.node.MineNewBlock(ctx)
		if err != nil {
			switch {
			case errors.Is(err, ErrNotEnoughTransactions):
				bw.evHandler("bcWorker: runMiningOperation: miningG: MineNewBlock: not enough transactions in mempool")
			case ctx.Err() != nil:
				bw.evHandler("bcWorker: runMiningOperation: miningG: MineNewBlock: mining cancelled")
			default:
				bw.evHandler(fmt.Sprintf("bcWorker: runMiningOperation: miningG: MineNewBlock: ERROR: %s", err))
			}
			return
		}

		// WOW, we mined a block.
		bw.evHandler(fmt.Sprintf("bcWorker: mineNewBlock: miningG: prevBlk[%s]: newBlk[%s]: numTrans[%d]", block.Header.PrevBlock, block.Hash(), len(block.Transactions)))

		// TODO: SEND NEW BLOCK TO THE CHAIN!!!!
	}()

	// Wait for both G's to terminate.
	wg.Wait()
}

// =============================================================================

// runPeerOperation updates the peer list and sync's up the database.
func (bw *bcWorker) runPeerOperation() {
	bw.evHandler("bcWorker: runPeerOperation: mainG: started")
	defer bw.evHandler("bcWorker: runPeerOperation: mainG: completed")

	for ipPort := range bw.node.CopyKnownPeersList() {

		// Retrieve the status of this peer.
		peer, err := bw.queryPeerStatus(ipPort)
		if err != nil {
			bw.evHandler(fmt.Sprintf("bcWorker: runPeerOperation: mainG: queryPeerStatus: ERROR: %s", err))
		}

		// Add new peers to this nodes list.
		if err := bw.addNewPeers(peer.KnownPeers); err != nil {
			bw.evHandler(fmt.Sprintf("bcWorker: runPeerOperation: mainG: addNewPeers: ERROR: %s", err))
		}

		// If this peer has blocks we don't have, we need to add them.
		if peer.LatestBlockNumber > bw.node.CopyLatestBlock().Header.Number {
			bw.evHandler(fmt.Sprintf("bcWorker: runPeerOperation: mainG: writePeerBlocks: latestBlockNumber[%d]", peer.LatestBlockNumber))
			if err := bw.writePeerBlocks(ipPort); err != nil {
				bw.evHandler(fmt.Sprintf("bcWorker: runPeerOperation: mainG: writePeerBlocks: ERROR %s", err))
			}
		}
	}
}

// queryPeerStatus looks for new nodes on the blockchain by asking
// known nodes for their peer list. New nodes are added to the list.
func (bw *bcWorker) queryPeerStatus(ipPort string) (peerStatus, error) {
	bw.evHandler("bcWorker: runPeerOperation: mainG: queryPeerStatus: started")
	defer bw.evHandler("bcWorker: runPeerOperation: mainG: queryPeerStatus: cempleted")

	url := fmt.Sprintf("http://%s/v1/node/status", ipPort)

	var client http.Client
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return peerStatus{}, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return peerStatus{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, err := io.ReadAll(resp.Body)
		if err != nil {
			return peerStatus{}, err
		}
		return peerStatus{}, errors.New(string(msg))
	}

	var peer peerStatus
	if err := json.NewDecoder(resp.Body).Decode(&peer); err != nil {
		return peerStatus{}, err
	}

	bw.evHandler(fmt.Sprintf("bcWorker: runPeerOperation: mainG: queryPeerStatus: node[%s]: latest-blknum[%d]: peer-list[%s]", ipPort, peer.LatestBlockNumber, peer.KnownPeers))

	return peer, nil
}

// addNewPeers takes the set of known peers and makes sure they are included
// in the nodes list of know peers.
func (bw *bcWorker) addNewPeers(knownPeers map[string]struct{}) error {
	bw.evHandler("bcWorker: runPeerOperation: mainG: addNewPeers: started")
	defer bw.evHandler("bcWorker: runPeerOperation: mainG: addNewPeers: cempleted")

	for ipPort := range knownPeers {
		if err := bw.node.addPeerNode(ipPort); err != nil {
			return err
		}
		bw.evHandler(fmt.Sprintf("bcWorker: runPeerOperation: mainG: addNewPeers: add peer nodes: adding node %s", ipPort))
	}

	return nil
}

// writePeerBlocks queries the specified node asking for blocks this
// node does not have.
func (bw *bcWorker) writePeerBlocks(ipPort string) error {
	bw.evHandler("bcWorker: runPeerOperation: mainG: writePeerBlocks: started")
	defer bw.evHandler("bcWorker: runPeerOperation: mainG: writePeerBlocks: cempleted")

	from := bw.node.CopyLatestBlock().Header.Number + 1
	url := fmt.Sprintf("http://%s/v1/blocks/list/%d/latest", ipPort, from)

	var client http.Client
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		bw.evHandler("bcWorker: runPeerOperation: mainG: writePeerBlocks: no new block")
		return nil
	}

	if resp.StatusCode != http.StatusOK {
		msg, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		return errors.New(string(msg))
	}

	var pbs []PeerBlock
	if err := json.NewDecoder(resp.Body).Decode(&pbs); err != nil {
		return err
	}

	for _, pb := range pbs {
		bw.evHandler(fmt.Sprintf("bcWorker: runPeerOperation: mainG: writePeerBlocks: prevBlk[%s]: newBlk[%s]: numTrans[%d]", pb.Header.PrevBlock, pb.Header.ThisBlock, len(pb.Transactions)))

		_, err := bw.node.writeNewBlockFromPeer(pb)
		if err != nil {
			return err
		}
	}

	return nil
}