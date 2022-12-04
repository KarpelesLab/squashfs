package squashfs

type Option func(sb *Superblock) error

func InodeOffset(inoOfft uint64) Option {
	return func(sb *Superblock) error {
		sb.inoOfft = inoOfft
		return nil
	}
}
