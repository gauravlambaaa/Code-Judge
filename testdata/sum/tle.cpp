// Never finishes: expect TLE
// (volatile stops the compiler from optimising the loop away.)
int main() {
    volatile unsigned long long x = 0;
    while (true) x++;
}
