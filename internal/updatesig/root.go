// internal/updatesig/root.go
// Why this exists: the root certificate every wedigo binary trusts for updates, written by the CA tool.
// Version: 2026.11.04
package updatesig

const rootPEM = `-----BEGIN CERTIFICATE-----
MIIBjTCCAT+gAwIBAgIQWjcDoIlJRjUs+dlslPJl9jAFBgMrZXAwRDEcMBoGA1UE
ChMTd2VkaWdvIGl0IHNvbHV0aW9uczEkMCIGA1UEAxMbd2VkaWdvIGl0IHNvbHV0
aW9ucyBSb290IENBMCAXDTI2MTEwMzA0MTYwNloYDzIwNTYxMDI3MDQxNjA2WjBE
MRwwGgYDVQQKExN3ZWRpZ28gaXQgc29sdXRpb25zMSQwIgYDVQQDExt3ZWRpZ28g
aXQgc29sdXRpb25zIFJvb3QgQ0EwKjAFBgMrZXADIQCpRzm7aTCRSq/CRRY0ii/2
WxUfHmzsGIsef+nb8u1zZaNFMEMwDgYDVR0PAQH/BAQDAgEGMBIGA1UdEwEB/wQI
MAYBAf8CAQEwHQYDVR0OBBYEFHDDgTP7U8eRyS4bmVwzBv8Wx5VcMAUGAytlcANB
AAIcmXKz7YJIJCvh9xWyFbSihav0TbUbK8RYAVDyQXkKZBK2DnjixZr8sEer+fnU
qBgAYtzLhjckklW6mq5syQs=
-----END CERTIFICATE-----
`
