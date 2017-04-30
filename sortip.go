package main

import (
	"log"
	"net"
)

type prefixes []net.IPNet
type nlris []nlri

func (slice prefixes) Len() int {
	return len(slice)
}

func (slice nlris) Len() int {
	return len(slice)
}

func (slice prefixes) Less(left, right int) bool {
	// compare the addresses a byte at a time
	for i := 0; i < len(slice[left].IP); i++ {
		switch {
		case slice[left].IP[i] < slice[right].IP[i]:
			return true
		case slice[left].IP[i] > slice[right].IP[i]:
			return false
		case slice[left].IP[i] == slice[right].IP[i]:
			continue
		}
	}

	// compare the netmask if everything is equal so far!
	for i := 0; i < len(slice[left].Mask); i++ {
		switch {
		case slice[left].Mask[i] < slice[right].Mask[i]:
			return true
		case slice[left].Mask[i] > slice[right].Mask[i]:
			return false
		case slice[left].Mask[i] == slice[right].Mask[i]:
			continue
		}
	}

	// we should never get here
	log.Fatal("Sorting fail, identical prefixes!")
	return false // actually won't get here, but vim-go complains otherwise
}

func (slice nlris) Less(left, right int) bool {
	// compare the nexthop address a byte at a time
	for i := 0; i < len(slice[left].nextHop); i++ {
		switch {
		case slice[left].nextHop[i] < slice[right].nextHop[i]:
			return true
		case slice[left].nextHop[i] > slice[right].nextHop[i]:
			return false
		case slice[left].nextHop[i] == slice[right].nextHop[i]:
			continue
		}
	}

	// compare the prefix addresses a byte at a time
	for i := 0; i < len(slice[left].prefix.IP); i++ {
		switch {
		case slice[left].prefix.IP[i] < slice[right].prefix.IP[i]:
			return true
		case slice[left].prefix.IP[i] > slice[right].prefix.IP[i]:
			return false
		case slice[left].prefix.IP[i] == slice[right].prefix.IP[i]:
			continue
		}
	}

	// compare the prefix netmask if everything is equal so far!
	for i := 0; i < len(slice[left].prefix.Mask); i++ {
		switch {
		case slice[left].prefix.Mask[i] < slice[right].prefix.Mask[i]:
			return true
		case slice[left].prefix.Mask[i] > slice[right].prefix.Mask[i]:
			return false
		case slice[left].prefix.Mask[i] == slice[right].prefix.Mask[i]:
			continue
		}
	}

	// we should never get here
	log.Fatal("Sorting fail, identical prefixes!")
	return false // actually won't get here, but vim-go complains otherwise
}

func (slice prefixes) Swap(left, right int) {
	slice[left], slice[right] = slice[right], slice[left]
}

func (slice nlris) Swap(left, right int) {
	slice[left], slice[right] = slice[right], slice[left]
}
