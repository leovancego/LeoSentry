//go:build linux

package nftctl

import (
	"errors"
	"fmt"
	"net/netip"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// IPv4 头中源/目的地址的偏移。
const (
	ipv4SrcOffset = 12
	ipv4DstOffset = 16
)

// nft 寄存器：拼接键的第一段放在 reg 1，第二段紧随其后放在 32 位寄存器 9。
const (
	regKey    = 1
	regConcat = 9
)

// Controller 管理 LeoSentry 独立的 nft 表 inet leosentry。
// 使用独立表而不改动 fw4 的表，fw4 reload 时不会冲掉这里的规则。
type Controller struct {
	conn    *nftables.Conn
	table   *nftables.Table
	control map[string]*nftables.Set
	prev    Targets
}

// New 创建 Controller。
func New() (*Controller, error) {
	conn, err := nftables.New(nftables.AsLasting())
	if err != nil {
		return nil, fmt.Errorf("nftables: %w", err)
	}
	return &Controller{
		conn:  conn,
		table: &nftables.Table{Name: TableName, Family: nftables.TableFamilyINet},
	}, nil
}

// Setup 以单个原子批次重建采集用 nft 表，等价于：
//
//	table inet leosentry {
//	    set managed_devices { type ipv4_addr; flags interval; elements = { <受管网段> } }
//	    set traffic_up   { type ipv4_addr . ipv4_addr; flags dynamic,timeout; timeout 1h; size 65535; }
//	    set traffic_down { type ipv4_addr . ipv4_addr; flags dynamic,timeout; timeout 1h; size 65535; }
//	    set block_all     { type ipv4_addr; }
//	    set block_all_mac { type ether_addr; }
//	    set block_video   { type ipv4_addr; }
//	    set block_game    { type ipv4_addr; }
//	    set game_dst      { type ipv4_addr; }
//	    set video_dst     { type ipv4_addr; }
//	    chain forward {
//	        type filter hook forward priority -10; policy accept;
//	        # 暂停同时按网卡地址丢弃，覆盖 IPv6。局域网桥接不进这条链。
//	        ether saddr @block_all_mac drop
//	        ether daddr @block_all_mac drop
//	        ip saddr @block_all drop
//	        ip daddr @block_all drop
//	        ip saddr @block_video ip daddr @video_dst drop
//	        ip daddr @block_video ip saddr @video_dst drop
//	        ip saddr @block_game ip daddr @game_dst drop
//	        ip daddr @block_game ip saddr @game_dst drop
//	        ip saddr @managed_devices update @traffic_up   { ip saddr . ip daddr counter }
//	        ip daddr @managed_devices update @traffic_down { ip daddr . ip saddr counter }
//	    }
//	}
//
// 两个流量集合的键都是"设备IP . 目标IP"，读出后可直接按键合并上下行。
// 表被重建后所有计数器从零开始，读取端据此把新出现的元素值直接当作增量。
func (c *Controller) Setup(opts Options) error {
	if len(opts.ManagedNetworks) == 0 {
		return errors.New("nftctl: no managed networks")
	}
	if err := c.deleteTableIfExists(); err != nil {
		return err
	}

	c.conn.AddTable(c.table)

	managed := &nftables.Set{
		Table:    c.table,
		Name:     SetManagedDevices,
		KeyType:  nftables.TypeIPAddr,
		Interval: true,
	}
	elems, err := intervalElements(opts.ManagedNetworks)
	if err != nil {
		return err
	}
	if err := c.conn.AddSet(managed, elems); err != nil {
		return fmt.Errorf("nftctl: add set %s: %w", managed.Name, err)
	}

	up := c.trafficSet(SetTrafficUp, opts)
	down := c.trafficSet(SetTrafficDown, opts)
	blockAll := c.addrSet(SetBlockAll)
	blockMAC := c.macSet(SetBlockAllMAC)
	blockVideo := c.addrSet(SetBlockVideo)
	blockGame := c.addrSet(SetBlockGame)
	gameDst := c.addrSet(SetGameDst)
	videoDst := c.addrSet(SetVideoDst)
	for _, s := range []*nftables.Set{up, down, blockAll, blockMAC, blockVideo, blockGame, gameDst, videoDst} {
		if err := c.conn.AddSet(s, nil); err != nil {
			return fmt.Errorf("nftctl: add set %s: %w", s.Name, err)
		}
	}
	c.control = map[string]*nftables.Set{
		SetBlockAll: blockAll, SetBlockAllMAC: blockMAC, SetBlockVideo: blockVideo, SetBlockGame: blockGame,
		SetGameDst: gameDst, SetVideoDst: videoDst,
	}
	c.prev = Targets{}

	policy := nftables.ChainPolicyAccept
	chain := c.conn.AddChain(&nftables.Chain{
		Name:     ChainForward,
		Table:    c.table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookForward,
		Priority: nftables.ChainPriorityRef(ForwardPriority),
		Policy:   &policy,
	})
	// 暂停按网卡地址丢弃转发，IPv4 和 IPv6 都会断。局域网桥接流量不进 forward 链。
	c.addRule(chain, dropEther(blockMAC, true))
	c.addRule(chain, dropEther(blockMAC, false))
	c.addRule(chain, dropIPv4(blockAll, true))
	c.addRule(chain, dropIPv4(blockAll, false))
	c.addRule(chain, blockDest(blockVideo, videoDst, true))
	c.addRule(chain, blockDest(blockVideo, videoDst, false))
	c.addRule(chain, blockDest(blockGame, gameDst, true))
	c.addRule(chain, blockDest(blockGame, gameDst, false))
	c.conn.AddRule(&nftables.Rule{Table: c.table, Chain: chain, Exprs: countRule(managed, up, ipv4SrcOffset, ipv4DstOffset)})
	c.conn.AddRule(&nftables.Rule{Table: c.table, Chain: chain, Exprs: countRule(managed, down, ipv4DstOffset, ipv4SrcOffset)})

	if err := c.conn.Flush(); err != nil {
		return fmt.Errorf("nftctl: setup table: %w", err)
	}
	return nil
}

// Teardown 删除采集用 nft 表。
func (c *Controller) Teardown() error {
	if err := c.deleteTableIfExists(); err != nil {
		return err
	}
	return c.conn.Flush()
}

// Close 关闭 netlink 连接。
func (c *Controller) Close() error {
	return c.conn.CloseLasting()
}

func (c *Controller) deleteTableIfExists() error {
	tables, err := c.conn.ListTablesOfFamily(nftables.TableFamilyINet)
	if err != nil {
		return fmt.Errorf("nftctl: list tables: %w", err)
	}
	for _, t := range tables {
		if t.Name == TableName {
			c.conn.DelTable(c.table)
			return nil
		}
	}
	return nil
}

func (c *Controller) addrSet(name string) *nftables.Set {
	return &nftables.Set{Table: c.table, Name: name, KeyType: nftables.TypeIPAddr}
}

func (c *Controller) macSet(name string) *nftables.Set {
	return &nftables.Set{Table: c.table, Name: name, KeyType: nftables.TypeEtherAddr}
}

func (c *Controller) addRule(chain *nftables.Chain, exprs []expr.Any) {
	c.conn.AddRule(&nftables.Rule{Table: c.table, Chain: chain, Exprs: exprs})
}

func (c *Controller) trafficSet(name string, opts Options) *nftables.Set {
	return &nftables.Set{
		Table:         c.table,
		Name:          name,
		KeyType:       nftables.MustConcatSetType(nftables.TypeIPAddr, nftables.TypeIPAddr),
		Concatenation: true,
		Dynamic:       true,
		HasTimeout:    true,
		Timeout:       opts.TrafficTimeout,
		Size:          opts.TrafficSetSize,
	}
}

func dropEther(set *nftables.Set, src bool) []expr.Any {
	off := uint32(0)
	if src {
		off = 6
	}
	return []expr.Any{
		&expr.Payload{DestRegister: regKey, Base: expr.PayloadBaseLLHeader, Offset: off, Len: 6},
		lookup(set, false),
		&expr.Verdict{Kind: expr.VerdictDrop},
	}
}

func dropIPv4(set *nftables.Set, src bool) []expr.Any {
	off := uint32(ipv4DstOffset)
	if src {
		off = ipv4SrcOffset
	}
	e := matchIPv4()
	e = append(e, payload(off)...)
	e = append(e, lookup(set, false))
	e = append(e, &expr.Verdict{Kind: expr.VerdictDrop})
	return e
}

func blockDest(device, dest *nftables.Set, deviceIsSrc bool) []expr.Any {
	devOff, otherOff := uint32(ipv4SrcOffset), uint32(ipv4DstOffset)
	if !deviceIsSrc {
		devOff, otherOff = ipv4DstOffset, ipv4SrcOffset
	}
	e := matchIPv4()
	e = append(e, payload(devOff)...)
	e = append(e, lookup(device, false))
	e = append(e, payload(otherOff)...)
	e = append(e, lookup(dest, false))
	e = append(e, &expr.Verdict{Kind: expr.VerdictDrop})
	return e
}

func matchIPv4() []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: regKey},
		&expr.Cmp{Op: expr.CmpOpEq, Register: regKey, Data: []byte{unix.NFPROTO_IPV4}},
	}
}

func payload(offset uint32) []expr.Any {
	return []expr.Any{&expr.Payload{DestRegister: regKey, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: 4}}
}

func lookup(set *nftables.Set, invert bool) expr.Any {
	return &expr.Lookup{SourceRegister: regKey, SetName: set.Name, SetID: set.ID, Invert: invert}
}

// countRule 生成：ip <deviceOff> @managed update @set { ip <deviceOff> . ip <targetOff> counter }
func countRule(managed, set *nftables.Set, deviceOff, targetOff uint32) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: regKey},
		&expr.Cmp{Op: expr.CmpOpEq, Register: regKey, Data: []byte{unix.NFPROTO_IPV4}},
		&expr.Payload{DestRegister: regKey, Base: expr.PayloadBaseNetworkHeader, Offset: deviceOff, Len: 4},
		&expr.Lookup{SourceRegister: regKey, SetName: managed.Name, SetID: managed.ID},
		&expr.Payload{DestRegister: regKey, Base: expr.PayloadBaseNetworkHeader, Offset: deviceOff, Len: 4},
		&expr.Payload{DestRegister: regConcat, Base: expr.PayloadBaseNetworkHeader, Offset: targetOff, Len: 4},
		&expr.Dynset{
			SrcRegKey: regKey,
			SetName:   set.Name,
			SetID:     set.ID,
			Operation: uint32(unix.NFT_DYNSET_OP_UPDATE),
			Exprs:     []expr.Any{&expr.Counter{}},
		},
	}
}

// intervalElements 将网段转为区间集合元素：起始地址 + 区间结束标记（结束地址为网段后第一个地址）。
func intervalElements(prefixes []netip.Prefix) ([]nftables.SetElement, error) {
	elems := make([]nftables.SetElement, 0, 2*len(prefixes))
	for _, p := range prefixes {
		p = p.Masked()
		if !p.Addr().Is4() {
			return nil, fmt.Errorf("nftctl: %s is not IPv4", p)
		}
		start := p.Addr().As4()
		end := lastAddr(p).Next()
		elems = append(elems, nftables.SetElement{Key: start[:]})
		if end.IsValid() {
			e := end.As4()
			elems = append(elems, nftables.SetElement{Key: e[:], IntervalEnd: true})
		}
	}
	return elems, nil
}

func lastAddr(p netip.Prefix) netip.Addr {
	a := p.Addr().As4()
	host := uint32(1)<<(32-p.Bits()) - 1
	v := uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])
	v |= host
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}
