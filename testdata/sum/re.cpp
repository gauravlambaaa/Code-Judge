// Reads through a null pointer: segmentation fault (exit code 139). Expect RE
#include <iostream>

int main() {
    int* volatile p = nullptr;
    std::cout << *p << "\n";
}
