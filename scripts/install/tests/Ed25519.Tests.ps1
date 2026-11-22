# Pester 5 tests for scripts/install/Ed25519.ps1. They must pass on Windows
# PowerShell 5.1 and on PowerShell 7 (CI runs both, proto-installer.yml).

BeforeAll {
    . (Join-Path $PSScriptRoot '..\Ed25519.ps1')

    function ConvertFrom-Hex([string]$Hex) {
        $bytes = New-Object byte[] ($Hex.Length / 2)
        for ($i = 0; $i -lt $bytes.Length; $i++) {
            $bytes[$i] = [Convert]::ToByte($Hex.Substring(2 * $i, 2), 16)
        }
        return , $bytes
    }

    # RFC 8032 section 7.1, "Test Vectors for Ed25519", TEST 1, TEST 2 and
    # TEST 3, copied exactly (PUBLIC KEY, MESSAGE, SIGNATURE).
    $script:Vectors = @(
        @{
            Name      = 'TEST 1'
            PublicKey = 'd75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a'
            Message   = ''
            Signature = 'e5564300c360ac729086e2cc806e828a84877f1eb8e5d974d873e065224901555fb8821590a33bacc61e39701cf9b46bd25bf5f0595bbe24655141438e7a100b'
        },
        @{
            Name      = 'TEST 2'
            PublicKey = '3d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c'
            Message   = '72'
            Signature = '92a009a9f0d4cab8720e820b5f642540a2b27b5416503f8fb3762223ebdb69da085ac1e43e15996e458f3613d0f11d8c387b2eaeb4302aeeb00d291612bb0c00'
        },
        @{
            Name      = 'TEST 3'
            PublicKey = 'fc51cd8e6218a1a38da47ed00230f0580816ed13ba3303ac5deb911548908025'
            Message   = 'af82'
            Signature = '6291d657deec24024827e69c3abe01a30ce548a284743a445e3680d7db5ac3ac18ff9b538d16f290ae67f760984dc6594a7c15e9716ed28dc027beceea1ec40a'
        }
    )

    # L, the order of the base point (RFC 8032 section 5.1).
    $script:L = [System.Numerics.BigInteger]::Parse('7237005577332262213973186563042994240857116359379907606001950938285454250989')

    # Encoding of the base point B, and of the identity (y = 1) in its
    # canonical form and with y = p + 1 (a non-canonical encoding: y >= p).
    $script:EncB = '5866666666666666666666666666666666666666666666666666666666666666'
    $script:EncIdentity = '0100000000000000000000000000000000000000000000000000000000000000'
    $script:EncIdentityNonCanonical = 'eeffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f'
    $script:ScalarOne = '0100000000000000000000000000000000000000000000000000000000000000'
    $script:ScalarZero = '0000000000000000000000000000000000000000000000000000000000000000'
    $script:Msg = [Text.Encoding]::ASCII.GetBytes('zatvor')
}

Describe 'Test-Ed25519Signature' {
    It 'accepts RFC 8032 section 7.1 <Name>' -TestCases @(
        @{ Index = 0; Name = 'TEST 1' }, @{ Index = 1; Name = 'TEST 2' }, @{ Index = 2; Name = 'TEST 3' }
    ) {
        $v = $script:Vectors[$Index]
        Test-Ed25519Signature -PublicKey (ConvertFrom-Hex $v.PublicKey) -Message (ConvertFrom-Hex $v.Message) -Signature (ConvertFrom-Hex $v.Signature) |
            Should -BeTrue
    }

    It 'rejects TEST 2 with one message bit flipped' {
        $v = $script:Vectors[1]
        $m = ConvertFrom-Hex $v.Message
        $m[0] = $m[0] -bxor 1
        Test-Ed25519Signature -PublicKey (ConvertFrom-Hex $v.PublicKey) -Message $m -Signature (ConvertFrom-Hex $v.Signature) |
            Should -BeFalse
    }

    It 'rejects TEST 1 with S replaced by S + L (S >= L)' {
        # S + L is the same scalar mod L, so only the S < L check rejects it.
        $v = $script:Vectors[0]
        $sig = ConvertFrom-Hex $v.Signature
        $sBytes = New-Object byte[] 33
        [Array]::Copy($sig, 32, $sBytes, 0, 32)
        $s = [System.Numerics.BigInteger]::new($sBytes) + $script:L
        $sPlusL = $s.ToByteArray()
        for ($i = 0; $i -lt 32; $i++) {
            $sig[32 + $i] = if ($i -lt $sPlusL.Length) { $sPlusL[$i] } else { 0 }
        }
        Test-Ed25519Signature -PublicKey (ConvertFrom-Hex $v.PublicKey) -Message (ConvertFrom-Hex $v.Message) -Signature $sig |
            Should -BeFalse
    }

    It 'control: canonical identity key with R = B, S = 1 verifies (RFC 8032 does not reject small-order keys)' {
        Test-Ed25519Signature -PublicKey (ConvertFrom-Hex $script:EncIdentity) -Message $script:Msg -Signature (ConvertFrom-Hex ($script:EncB + $script:ScalarOne)) |
            Should -BeTrue
    }

    It 'rejects the same signature when A is encoded non-canonically (y >= p)' {
        Test-Ed25519Signature -PublicKey (ConvertFrom-Hex $script:EncIdentityNonCanonical) -Message $script:Msg -Signature (ConvertFrom-Hex ($script:EncB + $script:ScalarOne)) |
            Should -BeFalse
    }

    It 'control: identity key with R = identity, S = 0 verifies' {
        Test-Ed25519Signature -PublicKey (ConvertFrom-Hex $script:EncIdentity) -Message $script:Msg -Signature (ConvertFrom-Hex ($script:EncIdentity + $script:ScalarZero)) |
            Should -BeTrue
    }

    It 'rejects the same signature when R is encoded non-canonically (y >= p)' {
        Test-Ed25519Signature -PublicKey (ConvertFrom-Hex $script:EncIdentity) -Message $script:Msg -Signature (ConvertFrom-Hex ($script:EncIdentityNonCanonical + $script:ScalarZero)) |
            Should -BeFalse
    }

    It 'rejects a public key that is not a curve point' {
        # y = 2 gives u/v with no square root mod p.
        $a = ConvertFrom-Hex '0200000000000000000000000000000000000000000000000000000000000000'
        $v = $script:Vectors[1]
        Test-Ed25519Signature -PublicKey $a -Message (ConvertFrom-Hex $v.Message) -Signature (ConvertFrom-Hex $v.Signature) |
            Should -BeFalse
    }

    It 'rejects wrong key and signature lengths' {
        $v = $script:Vectors[1]
        $pk = ConvertFrom-Hex $v.PublicKey
        $sig = ConvertFrom-Hex $v.Signature
        Test-Ed25519Signature -PublicKey $pk[0..30] -Message (ConvertFrom-Hex $v.Message) -Signature $sig | Should -BeFalse
        Test-Ed25519Signature -PublicKey $pk -Message (ConvertFrom-Hex $v.Message) -Signature $sig[0..62] | Should -BeFalse
    }
}
