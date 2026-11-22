package migrate

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"slices"
	"strings"
)

// node is an element of a small XML document, for example the description of a Plesk backup.
// The tree has the elements, their attributes and their text. It has no namespaces.
type node struct {
	name  string
	attr  map[string]string
	text  strings.Builder
	kids  []*node
	outer *node
}

// Limits of an XML document. The Go decoder does not read external entities.
const (
	maxXMLDepth = 64
	maxXMLNodes = 500000
)

// parseXML reads an XML document into a tree.
func parseXML(b []byte) (*node, error) {
	dec := xml.NewDecoder(bytes.NewReader(b))
	// A Plesk backup can declare an encoding other than UTF-8. The tree keeps the bytes.
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	root := &node{}
	cur, depth, count := root, 0, 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			count++
			if depth > maxXMLDepth || count > maxXMLNodes {
				return nil, errors.New("the XML document is too large")
			}
			n := &node{name: t.Name.Local, attr: map[string]string{}, outer: cur}
			for _, a := range t.Attr {
				n.attr[a.Name.Local] = a.Value
			}
			cur.kids = append(cur.kids, n)
			cur = n
		case xml.EndElement:
			depth--
			cur = cur.outer
		case xml.CharData:
			if cur != root {
				cur.text.Write(t)
			}
		}
	}
	if len(root.kids) != 1 {
		return nil, errors.New("the XML document has no root element")
	}
	return root.kids[0], nil
}

// child gives the first child element with the name, or nil.
func (n *node) child(name string) *node {
	if n == nil {
		return nil
	}
	for _, k := range n.kids {
		if k.name == name {
			return k
		}
	}
	return nil
}

// at gives the element at a path of child names, or nil.
func (n *node) at(names ...string) *node {
	for _, name := range names {
		n = n.child(name)
	}
	return n
}

// all gives the child elements with the name.
func (n *node) all(name string) []*node {
	if n == nil {
		return nil
	}
	var out []*node
	for _, k := range n.kids {
		if k.name == name {
			out = append(out, k)
		}
	}
	return out
}

// find gives each element below n with the name, in document order. It does not go into the
// elements with a name in skip.
func (n *node) find(name string, skip ...string) []*node {
	if n == nil {
		return nil
	}
	var out []*node
	for _, k := range n.kids {
		if k.name == name {
			out = append(out, k)
		}
		if !slices.Contains(skip, k.name) {
			out = append(out, k.find(name, skip...)...)
		}
	}
	return out
}

// value gives an attribute, or the text of a child element with the same name. A Plesk backup
// uses both forms for some values.
func (n *node) value(name string) string {
	if n == nil {
		return ""
	}
	if v, ok := n.attr[name]; ok {
		return strings.TrimSpace(v)
	}
	return n.child(name).content()
}

// content gives the text of the element.
func (n *node) content() string {
	if n == nil {
		return ""
	}
	return strings.TrimSpace(n.text.String())
}

// get gives an attribute, or "" for no element.
func (n *node) get(name string) string {
	if n == nil {
		return ""
	}
	return n.attr[name]
}
