package xz

// MaxDeclared is the largest LZMA2 dictionary size declared by any block
// header the reader has parsed. The fuzz targets use it to decide whether the
// reference decoder (which allocates what a header declares) may be run.
func (r *Reader) MaxDeclared() uint64 { return r.maxDeclared }
