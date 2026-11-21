package crypto

// IsGeneratedToken validates new personal/recovery credentials, not historical
// credentials presented for authentication.
func IsGeneratedToken(token string) bool {
	if len(token) != 64 {
		return false
	}
	for _, c := range token {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
