// Memory bomb: keeps allocating 10 MB blocks. Expect MLE
#include <cstring>
#include <vector>

int main() {
    std::vector<char*> blocks;
    while (true) {
        char* p = new char[10 << 20];
        // Linux hands out memory lazily: `new` only reserves address space,
        // and a page uses real memory the first time it's written. Without
        // this memset the program could "allocate" far more than 256 MB.
        std::memset(p, 1, 10 << 20);
        blocks.push_back(p);
    }
}
