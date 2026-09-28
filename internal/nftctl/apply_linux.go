//go:build linux

package nftctl

import (
	"fmt"
	"net"
	"net/netip"

	"github.com/google/nftables"
)

// Apply 把期望地址与上次成功下发的结果比较，没有变化时不产生任何 netlink 写入。
func (c *Controller) Apply(t Targets) (bool, error) {
	if len(c.control) == 0 {
		return false, fmt.Errorf("nftctl: table not set up")
	}
	ops, changed := Diff(c.prev, t)
	if !changed {
		return false, nil
	}
	if err := c.commit(ops); err != nil {
		return false, err
	}
	c.prev = Canon(t)
	return true, nil
}

func (c *Controller) commit(ops []SetOp) error {
	for _, op := range ops {
		set := c.control[op.Name]
		if set == nil {
			return fmt.Errorf("nftctl: missing set %s", op.Name)
		}
		add, del := ipElements(op.Add), ipElements(op.Del)
		if op.Name == SetBlockAllMAC {
			var err error
			if add, err = macElements(op.AddMAC); err != nil {
				return err
			}
			if del, err = macElements(op.DelMAC); err != nil {
				return err
			}
		}
		if len(add) > 0 {
			if err := c.conn.SetAddElements(set, add); err != nil {
				return fmt.Errorf("nftctl: add %s: %w", op.Name, err)
			}
		}
		if len(del) > 0 {
			if err := c.conn.SetDeleteElements(set, del); err != nil {
				return fmt.Errorf("nftctl: delete %s: %w", op.Name, err)
			}
		}
	}
	if err := c.conn.Flush(); err != nil {
		return fmt.Errorf("nftctl: update policy sets: %w", err)
	}
	return nil
}

func macElements(macs []string) ([]nftables.SetElement, error) {
	elems := make([]nftables.SetElement, 0, len(macs))
	for _, m := range macs {
		hw, err := net.ParseMAC(m)
		if err != nil || len(hw) != 6 {
			return nil, fmt.Errorf("nftctl: bad mac %s", m)
		}
		key := make([]byte, 6)
		copy(key, hw)
		elems = append(elems, nftables.SetElement{Key: key})
	}
	return elems, nil
}

func ipElements(ips []netip.Addr) []nftables.SetElement {
	elems := make([]nftables.SetElement, 0, len(ips))
	for _, ip := range ips {
		b := ip.As4()
		elems = append(elems, nftables.SetElement{Key: []byte{b[0], b[1], b[2], b[3]}})
	}
	return elems
}
