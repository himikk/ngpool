package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math/big"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/pkg/errors"
	"github.com/seehuhn/sha256d"

	"github.com/icook/ngpool/pkg/common"
	"github.com/icook/ngpool/pkg/service"
)

type Job struct {
	MainChainJob
	heights   map[string]int64
	auxChains []*AuxChainJob
	algo      *service.Algo
}

func NewJobFromTemplates(templates map[TemplateKey][]byte, algo *service.Algo) (*Job, error) {
	var (
		mainJobSet      bool
		mainJobTemplate *BlockTemplate
	)
	job := Job{
		heights: map[string]int64{},
		algo:    algo,
	}
	for tmplKey, tmplRaw := range templates {
		var tmpl BlockTemplate
		err := json.Unmarshal(tmplRaw, &tmpl)
		if err != nil {
			return nil, errors.Wrapf(err, "Unable to deserialize template: %v", string(tmplRaw))
		}
		chainConfig, ok := service.CurrencyConfig[tmplKey.Currency]
		if !ok {
			return nil, errors.Errorf("No currency config for %s", tmplKey.Currency)
		}

		switch tmplKey.TemplateType {
		case "getblocktemplate_aux":
			auxChainJob, err := NewAuxChainJob(&tmpl, chainConfig, algo)
			if err != nil {
				return nil, err
			}
			job.heights[chainConfig.Code] = auxChainJob.height
			job.auxChains = append(job.auxChains, auxChainJob)
		case "getblocktemplate":
			if mainJobSet {
				return nil, errors.Errorf("You can only have one base currency template")
			}
			mainJobSet = true
			mainChainJob, err := NewMainChainJob(&tmpl, chainConfig, algo)
			if err != nil {
				return nil, err
			}
			job.heights[chainConfig.Code] = mainChainJob.height
			job.MainChainJob = *mainChainJob
			mainJobTemplate = &tmpl
		default:
			return nil, errors.Errorf("Unrecognized TemplateType %s", tmplKey.TemplateType)
		}
	}
	if !mainJobSet {
		return nil, errors.New("Must have a main chain template")
	}

	// Build the merge mining merkle tree
	var merkleSize = 1
	var merkleBase [][]byte
	var merkleNonce uint32 = 0
MerkleLoop:
	for {
		// A candidate for the size of our blockchain merkle tree. If it fails
		// we iterate
		merkleBase = make([][]byte, merkleSize)
		for _, mj := range job.auxChains {
			var slot uint32 = merkleNonce
			slot = slot*1103515245 + 12345
			slot += uint32(mj.chainID)
			slot = slot*1103515245 + 12345
			slotNum := slot % uint32(merkleSize)
			if merkleBase[slotNum] != nil {
				merkleSize *= 2
				continue MerkleLoop
			}
			merkleBase[slotNum] = mj.headerHash.CloneBytes()
		}
		break
	}

	for _, mj := range job.auxChains {
		branch, mask := auxMerkleBranch(merkleBase, mj.headerHash.CloneBytes())
		mj.blockchainMerkleBranch = branch
		mj.blockchainMerkleMask = mask
	}

	mmCoinbase := bytes.Buffer{}
	if len(job.auxChains) > 0 {
		mmCoinbase.Write([]byte{0xfa, 0xbe, 'm', 'm'})
		if len(job.auxChains) > 1 {
			merkleRoot := merkleRoot(merkleBase)
			common.ReverseBytes(merkleRoot)
			mmCoinbase.Write(merkleRoot)
		} else {
			mj := job.auxChains[0]
			merkleRoot := mj.headerHash.CloneBytes()
			common.ReverseBytes(merkleRoot)
			mmCoinbase.Write(merkleRoot)
		}
		// Merkle size
		encodedMerkleSize := make([]byte, 4)
		binary.LittleEndian.PutUint32(encodedMerkleSize[0:], uint32(merkleSize))
		mmCoinbase.Write(encodedMerkleSize)
		// Nonce
		encodedNonce := make([]byte, 4)
		binary.LittleEndian.PutUint32(encodedNonce, uint32(merkleNonce))
		mmCoinbase.Write(encodedNonce)
	}

	coinbase1, coinbase2, err := mainJobTemplate.createCoinbaseSplit(job.currencyConfig, mmCoinbase.Bytes())
	if err != nil {
		return nil, errors.Wrap(err, "Unable to create coinbase")
	}
	job.coinbase1 = coinbase1
	job.coinbase2 = coinbase2
	return &job, nil
}

func (j *Job) SetFlush(lastJobSetFlush interface{}) (bool, interface{}) {
	switch prev := lastJobSetFlush.(type) {
	case map[string]int64:
		if j.height > prev[j.currencyConfig.Code] {
			j.cleanJobs = true
			return false, j.height
		} else if j.height < prev[j.currencyConfig.Code] {
			return true, nil
		}
		for _, aux := range j.auxChains {
			if aux.currencyConfig.FlushAux && aux.height > prev[aux.currencyConfig.Code] {
				j.cleanJobs = true
				return false, j.heights
			}
		}
	}
	return false, j.heights
}

func (j *Job) GetStratum2Params(extranonce1 []byte) (map[string]interface{}, error) {
	coinbase := bytes.Buffer{}
	coinbase.Write(j.coinbase1)
	coinbase.Write(extranonce1)
	// Empty bytes to fill in user selected extranonce2. Easier to do this than
	// conditionally change extranonce placeholder for jsonrpc 2, since users
	// don't pick extranonces in jsonrpc 2 (XMR)
	coinbase.Write([]byte{0, 0, 0, 0})
	coinbase.Write(j.coinbase2)

	var hasher = sha256d.New()
	hasher.Write(coinbase.Bytes())
	coinbaseHash := hasher.Sum(nil)

	header := j.GetBlockHeader([]byte{0, 0, 0, 0}, coinbaseHash)

	return map[string]interface{}{
		"blob": hex.EncodeToString(header[:76]),
	}, nil
}

func (j *Job) GetZCashStratumParams() ([]interface{}, error) {
	coinbase := bytes.Buffer{}
	coinbase.Write(j.coinbase1)
	// Empty bytes to fill in user selected extranonce2. Easier to do this than
	// conditionally change extranonce placeholder for zcash
	coinbase.Write([]byte{0, 0, 0, 0, 0, 0, 0, 0})
	coinbase.Write(j.coinbase2)

	var hasher = sha256d.New()
	hasher.Write(coinbase.Bytes())
	coinbaseHash := hasher.Sum(nil)

	// Hash the coinbase, then walk down the merkle branch to get merkle root
	rootHash := coinbaseHash

	for _, branch := range j.merkleBranch {
		hasher.Write(rootHash)
		hasher.Write(branch)
		rootHash = hasher.Sum(nil)
		hasher.Reset()
	}

	return []interface{}{
		hex.EncodeToString(j.version),
		hex.EncodeToString(j.prevBlockHash),
		hex.EncodeToString(rootHash),
		"0000000000000000000000000000000000000000000000000000000000000000",
		hex.EncodeToString(j.time),
		hex.EncodeToString(j.bits),
		j.cleanJobs,
	}, nil
}

func (j *Job) GetStratumParams() ([]interface{}, error) {
	var mb = []string{}
	for _, b := range j.merkleBranch {
		mb = append(mb, hex.EncodeToString(b))
	}
	return []interface{}{
		hex.EncodeToString(j.prevBlockHash),
		hex.EncodeToString(j.coinbase1),
		hex.EncodeToString(j.coinbase2),
		mb,
		hex.EncodeToString(j.version),
		hex.EncodeToString(j.bits),
		hex.EncodeToString(j.time),
		j.cleanJobs,
	}, nil
}

// This solve type is for equihash, which takes a different format than regular
// stratum submissions
type SolutionSolve struct {
	nonce2   []byte
	nTime    []byte
	nonce1   []byte
	solution []byte
}

func (m SolutionSolve) GetKey() string {
	return string(m.nonce2) + string(m.solution) + string(m.nTime)
}

func (j *Job) checkSolSolve(solveData SolutionSolve, shareTarget *big.Int) (map[string]*BlockSolve, bool, []string, error) {
	var ret = map[string]*BlockSolve{}
	var validShare = false

	coinbase := bytes.Buffer{}
	coinbase.Write(j.coinbase1)
	coinbase.Write([]byte{0, 0, 0, 0, 0, 0, 0, 0})
	coinbase.Write(j.coinbase2)

	var hasher = sha256d.New()
	hasher.Write(coinbase.Bytes())
	coinbaseHash := hasher.Sum(nil)
	hasher.Reset()

	buf := bytes.Buffer{}
	buf.Write(j.version)
	buf.Write(j.prevBlockHash)

	// walk down the merkle branch to get merkle root
	rootHash := coinbaseHash

	for _, branch := range j.merkleBranch {
		hasher.Write(rootHash)
		hasher.Write(branch)
		rootHash = hasher.Sum(nil)
		hasher.Reset()
	}

	buf.Write(rootHash)
	buf.Write([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	buf.Write(solveData.nTime)
	buf.Write(j.bits)
	buf.Write(solveData.nonce1)
	buf.Write(solveData.nonce2)
	buf.Write(solveData.solution)

	header := buf.Bytes()
	headerHsh, err := service.AlgoConfig["sha256d"].PoWHash(header)
	if err != nil {
		return nil, false, nil, err
	}
	hashObj, err := chainhash.NewHash(headerHsh)
	if err != nil {
		return nil, false, nil, err
	}
	bigHsh := blockchain.HashToBig(hashObj)
	// Share targets are in opposite endian of block targets (i think..), so
	// the comparison direction is opposite as well. Here we check if hash >
	// target, but below we check if hash < network_target
	if shareTarget != nil && bigHsh.Cmp(shareTarget) >= 0 {
		validShare = true
	}

	var currencies = []string{j.currencyConfig.Code}
	if bigHsh.Cmp(j.target) <= 0 {
		ret[j.currencyConfig.Code] = &BlockSolve{
			data:           j.GetBlock(header, coinbase.Bytes()),
			coinbaseHash:   coinbaseHash,
			subsidyAddress: (*j.currencyConfig.BlockSubsidyAddress).String(),
			powalgo:        j.algo.Name,
			subsidy:        j.subsidy,
			height:         j.height,
			powhash:        bigHsh,
			target:         j.target,
		}
	}

	for _, mj := range j.auxChains {
		currencies = append(currencies, mj.currencyConfig.Code)
		if bigHsh.Cmp(mj.target) <= 0 {
			ret[mj.currencyConfig.Code] = &BlockSolve{
				data:           mj.GetBlock(coinbase.Bytes(), headerHsh, j.merkleBranch, header),
				subsidy:        mj.subsidy,
				height:         mj.height,
				coinbaseHash:   mj.coinbaseHash,
				subsidyAddress: (*mj.currencyConfig.BlockSubsidyAddress).String(),
				powhash:        bigHsh,
				target:         mj.target,
			}
		}
	}
	return ret, validShare, currencies, nil
}

type ExtranonceSolve struct {
	nonce       []byte
	nTime       []byte
	extraNonce2 []byte
	extraNonce1 []byte
}

func (m ExtranonceSolve) GetKey() string {
	return string(m.extraNonce2) + string(m.extraNonce1) + string(m.nTime) + string(m.nonce)
}

func (j *Job) checkExtranonceSolve(solveData ExtranonceSolve, shareTarget *big.Int) (map[string]*BlockSolve, bool, []string, error) {
	var ret = map[string]*BlockSolve{}
	var validShare = false

	coinbase := bytes.Buffer{}
	coinbase.Write(j.coinbase1)
	coinbase.Write(solveData.extraNonce1)
	coinbase.Write(solveData.extraNonce2)
	coinbase.Write(j.coinbase2)

	var hasher = sha256d.New()
	hasher.Write(coinbase.Bytes())
	coinbaseHash := hasher.Sum(nil)

	header := j.GetBlockHeader(solveData.nonce, coinbaseHash)
	headerHsh, err := j.algo.PoWHash(header)
	if err != nil {
		return nil, false, nil, err
	}
	hashObj, err := chainhash.NewHash(headerHsh)
	if err != nil {
		return nil, false, nil, err
	}
	bigHsh := blockchain.HashToBig(hashObj)
	// Share targets are in opposite endian of block targets (i think..), so
	// the comparison direction is opposite as well. Here we check if hash >
	// target, but below we check if hash < network_target
	if shareTarget != nil && bigHsh.Cmp(shareTarget) >= 0 {
		validShare = true
	}

	var currencies = []string{j.currencyConfig.Code}
	if bigHsh.Cmp(j.target) <= 0 {
		ret[j.currencyConfig.Code] = &BlockSolve{
			data:           j.GetBlock(header, coinbase.Bytes()),
			coinbaseHash:   coinbaseHash,
			subsidyAddress: (*j.currencyConfig.BlockSubsidyAddress).String(),
			powalgo:        j.algo.Name,
			subsidy:        j.subsidy,
			height:         j.height,
			powhash:        bigHsh,
			target:         j.target,
		}
	}

	for _, mj := range j.auxChains {
		currencies = append(currencies, mj.currencyConfig.Code)
		if bigHsh.Cmp(mj.target) <= 0 {
			ret[mj.currencyConfig.Code] = &BlockSolve{
				data:           mj.GetBlock(coinbase.Bytes(), headerHsh, j.merkleBranch, header),
				subsidy:        mj.subsidy,
				height:         mj.height,
				coinbaseHash:   mj.coinbaseHash,
				subsidyAddress: (*mj.currencyConfig.BlockSubsidyAddress).String(),
				powhash:        bigHsh,
				target:         mj.target,
			}
		}
	}
	return ret, validShare, currencies, nil
}

func (j *Job) CheckSolves(solveData interface{}, shareTarget *big.Int) (map[string]*BlockSolve, bool, []string, error) {
	switch v := solveData.(type) {
	case ExtranonceSolve:
		return j.checkExtranonceSolve(v, shareTarget)
	case SolutionSolve:
		return j.checkSolSolve(v, shareTarget)
	}
	return nil, false, nil, errors.New("Unrecognized solve data")
}

type MainChainJob struct {
	currencyConfig *service.ChainConfig
	// For saving to database on solve
	subsidy int64
	height  int64

	// For making the block header for mining/solve
	bits          []byte
	time          []byte
	version       []byte
	prevBlockHash []byte
	coinbase1     []byte
	coinbase2     []byte
	merkleBranch  [][]byte

	// For checking solve and submitblock encoding
	target       *big.Int
	transactions [][]byte

	// for miners
	cleanJobs bool
}

func setAlgoVersion(version uint32, config *service.ChainConfig, algo *service.Algo) uint32 {
	if config.MultiAlgo {
		algoCode := config.MultiAlgoMap[algo.Name]
		// Clear all algo bits
		version &= ^uint32(((2 ^ config.MultiAlgoBitWidth) - 1) << config.MultiAlgoBitShift)
		// Inject algo bits for desired algo
		version |= (algoCode << config.MultiAlgoBitShift)
	}
	return version
}

func NewMainChainJob(tmpl *BlockTemplate, config *service.ChainConfig,
	algo *service.Algo) (*MainChainJob, error) {
	target, err := tmpl.getTarget()
	if err != nil {
		return nil, errors.Wrap(err, "Error generating target")
	}

	encodedTime := make([]byte, 4)
	binary.LittleEndian.PutUint32(encodedTime[0:], uint32(tmpl.CurTime))
	encodedVersion := make([]byte, 4)
	version := uint32(tmpl.Version)
	version = setAlgoVersion(version, config, algo)
	binary.LittleEndian.PutUint32(encodedVersion[0:], version)

	encodedPrevBlockHash, err := hex.DecodeString(tmpl.PreviousBlockhash)
	if err != nil {
		return nil, errors.Wrap(err, "Invalid PreviousBlockhash")
	}
	common.ReverseBytes(encodedPrevBlockHash)

	encodedBits, err := hex.DecodeString(tmpl.Bits)
	if err != nil {
		return nil, errors.Wrap(err, "Invalid bits")
	}
	common.ReverseBytes(encodedBits)

	transactions := [][]byte{}
	for _, tx := range tmpl.Transactions {
		decoded, err := hex.DecodeString(tx.Data)
		if err != nil {
			return nil, errors.Wrap(err, "Invalid data from txn")
		}
		transactions = append(transactions, decoded)
	}

	job := &MainChainJob{
		height:  tmpl.Height,
		subsidy: tmpl.CoinbaseValue,

		currencyConfig: config,
		transactions:   transactions,
		bits:           encodedBits,
		time:           encodedTime,
		version:        encodedVersion,
		prevBlockHash:  encodedPrevBlockHash,
		target:         target,
		merkleBranch:   tmpl.merkleBranch(),
		cleanJobs:      true, // TODO: change me
	}
	return job, nil
}

func (j *MainChainJob) GetBlockHeader(nonce []byte, coinbaseHash []byte) []byte {
	var hasher = sha256d.New()
	buf := bytes.Buffer{}
	buf.Write(j.version)
	buf.Write(j.prevBlockHash)

	// Hash the coinbase, then walk down the merkle branch to get merkle root
	rootHash := coinbaseHash

	for _, branch := range j.merkleBranch {
		hasher.Write(rootHash)
		hasher.Write(branch)
		rootHash = hasher.Sum(nil)
		hasher.Reset()
	}

	buf.Write(rootHash)
	buf.Write(j.time)
	buf.Write(j.bits)
	buf.Write(nonce)

	return buf.Bytes()
}

func (j *MainChainJob) GetBlock(header []byte, coinbase []byte) []byte {
	block := bytes.Buffer{}
	block.Write(header)
	wire.WriteVarInt(&block, 0, uint64(len(j.transactions)+1))
	block.Write(coinbase)

	for _, t := range j.transactions {
		block.Write(t)
	}
	return block.Bytes()
}

type AuxChainJob struct {
	currencyConfig *service.ChainConfig
	// For saving to database on solve
	subsidy int64
	height  int64

	// For checking solve and submitblock encoding
	headerHash             *chainhash.Hash
	blockHeader            []byte
	chainID                int
	blockchainMerkleBranch [][]byte
	blockchainMerkleMask   uint32
	transactions           [][]byte
	coinbase               []byte
	coinbaseHash           []byte
	target                 *big.Int
}

func NewAuxChainJob(template *BlockTemplate, config *service.ChainConfig,
	algo *service.Algo) (*AuxChainJob, error) {
	target, err := template.getTarget()
	if err != nil {
		return nil, errors.Wrap(err, "Error generating target")
	}

	blkHeader := bytes.Buffer{}
	encodedVersion := make([]byte, 4)
	version := uint32(template.Version)
	// Set flag for an AuxPoW block
	version |= (1 << 8)
	version |= (uint32(template.Extras.ChainID) << 16)
	version = setAlgoVersion(version, config, algo)
	binary.LittleEndian.PutUint32(encodedVersion[0:], version)
	blkHeader.Write(encodedVersion)

	encodedPrevBlockHash, err := hex.DecodeString(template.PreviousBlockhash)
	if err != nil {
		return nil, errors.Wrap(err, "Invalid PreviousBlockhash")
	}
	common.ReverseBytes(encodedPrevBlockHash)
	blkHeader.Write(encodedPrevBlockHash)

	// Hash the coinbase, then create a merkleRoot for the header from the
	// transaction hashes
	coinbase, err := template.createCoinbase(config, []byte{})
	if err != nil {
		return nil, err
	}
	var hasher = sha256d.New()
	hasher.Write(coinbase)
	coinbaseHash := hasher.Sum(nil)
	merkleRoot := template.merkleRoot(coinbaseHash)
	blkHeader.Write(merkleRoot)

	encodedTime := make([]byte, 4)
	binary.LittleEndian.PutUint32(encodedTime[0:], uint32(template.CurTime))
	blkHeader.Write(encodedTime)

	encodedBits, err := hex.DecodeString(template.Bits)
	if err != nil {
		return nil, errors.Wrap(err, "Invalid bits")
	}
	common.ReverseBytes(encodedBits)
	blkHeader.Write(encodedBits)
	blkHeader.Write([]byte{0, 0, 0, 0})

	hasher.Reset()
	hasher.Write(blkHeader.Bytes())
	hashObj, err := chainhash.NewHash(hasher.Sum(nil))
	if err != nil {
		return nil, err
	}

	transactions := [][]byte{}
	for _, tx := range template.Transactions {
		decoded, err := hex.DecodeString(tx.Data)
		if err != nil {
			return nil, errors.Wrap(err, "Invalid data from txn")
		}
		transactions = append(transactions, decoded)
	}

	if template.Extras.ChainID == 0 {
		return nil, errors.New("Null chainid")
	}

	acj := &AuxChainJob{
		height:  template.Height,
		subsidy: template.CoinbaseValue,

		currencyConfig: config,
		target:         target,
		coinbase:       coinbase,
		coinbaseHash:   coinbaseHash,
		transactions:   transactions,
		chainID:        template.Extras.ChainID,
		headerHash:     hashObj,
		blockHeader:    blkHeader.Bytes(),
	}
	return acj, nil
}
func (j *AuxChainJob) GetBlock(coinbase []byte, parentHash []byte, coinbaseBranch [][]byte, parentHeader []byte) []byte {
	block := bytes.Buffer{}
	block.Write(j.blockHeader)
	block.Write(coinbase)
	block.Write(parentHash)
	// Coinbase merkle branch
	wire.WriteVarInt(&block, 0, uint64(len(coinbaseBranch)))
	for _, branch := range coinbaseBranch {
		block.Write(branch)
	}
	// Coinbase branch mask is always all zeros (right, right, right...)
	block.Write([]byte{0, 0, 0, 0})

	// Blockchain merkle branch
	wire.WriteVarInt(&block, 0, uint64(len(j.blockchainMerkleBranch)))
	for _, branch := range j.blockchainMerkleBranch {
		block.Write(branch)
	}
	// Coinbase branch mask is always all zeros (right, right, right...)
	encodedMask := make([]byte, 4)
	binary.LittleEndian.PutUint32(encodedMask, j.blockchainMerkleMask)
	block.Write(encodedMask)

	block.Write(parentHeader)
	wire.WriteVarInt(&block, 0, uint64(len(j.transactions)+1))
	block.Write(j.coinbase)

	for _, t := range j.transactions {
		block.Write(t)
	}
	return block.Bytes()
}
