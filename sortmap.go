/*
 * sortmap.go
 *
 * This file contains a few functions for sorting a map by its integer
 * value.
 *
 */

package main

import "sort"

type sortedIntMap struct {
	m map[string]int
	s []string
}

func (sm *sortedIntMap) Len() int {
	return len(sm.m)
}

func (sm *sortedIntMap) Less(i, j int) bool {
	return sm.m[sm.s[i]] > sm.m[sm.s[j]]
}

func (sm *sortedIntMap) Swap(i, j int) {
	sm.s[i], sm.s[j] = sm.s[j], sm.s[i]
}

func sortedIntKeys(m map[string]int) []string {
	sm := new(sortedIntMap)
	sm.m = m
	sm.s = make([]string, len(m))
	i := 0
	for key := range m {
		sm.s[i] = key
		i++
	}
	sort.Sort(sm)
	return sm.s
}

// Reverse sort a map of float64s.
type sortedRevFloat64Map struct {
	m map[string]float64
	s []string
}

func (sm *sortedRevFloat64Map) Len() int {
	return len(sm.m)
}

func (sm *sortedRevFloat64Map) Less(i, j int) bool {
	return sm.m[sm.s[i]] < sm.m[sm.s[j]]
}

func (sm *sortedRevFloat64Map) Swap(i, j int) {
	sm.s[i], sm.s[j] = sm.s[j], sm.s[i]
}

func sortedRevFloat64Keys(m map[string]float64) []string {
	sm := new(sortedRevFloat64Map)
	sm.m = m
	sm.s = make([]string, len(m))
	i := 0
	for key := range m {
		sm.s[i] = key
		i++
	}
	sort.Sort(sm)
	return sm.s
}
