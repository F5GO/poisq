// Package search реализует интеллектуальный поиск, смену раскладки,
// транслитерацию и нечеткое сопоставление поисковых запросов для Navidrome.
//
// Данный файл trie.go реализует высокопроизводительное префиксное дерево (Trie),
// оптимизированное под мгновенный автокомплит и ранжирование подсказок по весу популярности.
package search

import (
	"sort"
	"strings"
)

// TrieMatch представляет результат поиска по префиксу.
type TrieMatch struct {
	Word         string
	Canonical    string
	ParentEntity string
	Weight       int
}

// trieNode представляет узел префиксного дерева.
type trieNode struct {
	children map[rune]*trieNode
	isEnd    bool
	matches  []TrieMatch // Хранит топ совпадений для мгновенного доступа без обхода поддерева
}

func newTrieNode() *trieNode {
	return &trieNode{
		children: make(map[rune]*trieNode),
	}
}

// Trie представляет потокобезопасную или read-heavy структуру префиксного дерева.
type Trie struct {
	root *trieNode
}

// NewTrie создает новое пустое префиксное дерево.
func NewTrie() *Trie {
	return &Trie{
		root: newTrieNode(),
	}
}

// Insert добавляет слово в дерево с соответствующим каноническим значением, родительской сущностью и весом.
func (t *Trie) Insert(word, canonical, parentEntity string, weight int) {
	cleanWord := strings.ToLower(strings.TrimSpace(word))
	if cleanWord == "" {
		return
	}

	match := TrieMatch{
		Word:         cleanWord,
		Canonical:    canonical,
		ParentEntity: parentEntity,
		Weight:       weight,
	}

	curr := t.root
	t.updateTopMatches(curr, match)

	for _, r := range cleanWord {
		child, exists := curr.children[r]
		if !exists {
			child = newTrieNode()
			curr.children[r] = child
		}
		curr = child
		t.updateTopMatches(curr, match)
	}
	curr.isEnd = true
}

// updateTopMatches сохраняет топ кандидатов в узле для быстродействия O(prefix_length).
func (t *Trie) updateTopMatches(node *trieNode, match TrieMatch) {
	for i, m := range node.matches {
		if m.Canonical == match.Canonical && m.ParentEntity == match.ParentEntity {
			if match.Weight > m.Weight {
				node.matches[i].Weight = match.Weight
			}
			t.sortMatches(node.matches)
			return
		}
	}

	node.matches = append(node.matches, match)
	t.sortMatches(node.matches)

	const maxCachedPerNode = 20
	if len(node.matches) > maxCachedPerNode {
		node.matches = node.matches[:maxCachedPerNode]
	}
}

func (t *Trie) sortMatches(matches []TrieMatch) {
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Weight > matches[j].Weight
	})
}

// SearchPrefix находит все подходящие канонические значения по префиксу.
func (t *Trie) SearchPrefix(prefix string, limit int) []TrieMatch {
	cleanPrefix := strings.ToLower(strings.TrimSpace(prefix))
	if cleanPrefix == "" {
		return nil
	}

	curr := t.root
	for _, r := range cleanPrefix {
		child, exists := curr.children[r]
		if !exists {
			return nil
		}
		curr = child
	}

	if limit <= 0 || limit > len(curr.matches) {
		limit = len(curr.matches)
	}

	result := make([]TrieMatch, limit)
	copy(result, curr.matches[:limit])
	return result
}
