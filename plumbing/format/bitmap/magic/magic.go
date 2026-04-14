package magic

import (
	"io"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/format/packfile"
	"github.com/go-git/go-git/v6/plumbing/storer"
)

type Storer struct {
	pf *packfile.Packfile
}

var _ storer.EncodedObjectStorer = (*Storer)(nil)

func NewMagicStorer(pf *packfile.Packfile) storer.EncodedObjectStorer {
	return &Storer{pf: pf}
}

func (m Storer) EncodedObject(objectType plumbing.ObjectType, hash plumbing.Hash) (plumbing.EncodedObject, error) {
	return m.pf.Get(hash)
}

func (m Storer) RawObjectWriter(typ plumbing.ObjectType, sz int64) (w io.WriteCloser, err error) {
	panic("implement me")
}

func (m Storer) NewEncodedObject() plumbing.EncodedObject {
	panic("implement me")
}

func (m Storer) SetEncodedObject(object plumbing.EncodedObject) (plumbing.Hash, error) {
	panic("implement me")
}

func (m Storer) IterEncodedObjects(objectType plumbing.ObjectType) (storer.EncodedObjectIter, error) {
	panic("implement me")
}

func (m Storer) HasEncodedObject(hash plumbing.Hash) error {
	panic("implement me")
}

func (m Storer) EncodedObjectSize(hash plumbing.Hash) (int64, error) {
	panic("implement me")
}

func (m Storer) AddAlternate(remote string) error {
	panic("implement me")
}
