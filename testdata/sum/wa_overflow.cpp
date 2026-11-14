// Uses int (32 bits, at most about 2.1 billion) instead of long long. It
// passes the small tests, but on test 3 the sum is 3 billion, which doesn't
// fit, so it overflows and prints a wrong number. Expect WA on test 3.
#include <iostream>

int main() {
    int a, b;
    std::cin >> a >> b;
    std::cout << a + b << "\n";
}
