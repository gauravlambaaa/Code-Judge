// A "compile bomb". #include pastes a file into the source code, and
// /dev/zero is a fake file that never ends: it returns zero bytes forever.
// So the compiler would read (and use memory) until something stops it.
// The compile step's own sandbox limits do. Expect CE
#include "/dev/zero"

int main() {}
