// Missing semicolon after `int a, b`: expect CE
#include <iostream>

int main() {
    int a, b
    std::cin >> a >> b;
    std::cout << a + b << "\n";
}
