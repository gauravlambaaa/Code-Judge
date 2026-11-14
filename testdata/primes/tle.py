# Trial division: correct, and fine for the small tests, but far too slow
# for n = 5,000,000. Expect TLE on test 4
n = int(input())
count = 0
for k in range(2, n + 1):
    d = 2
    while d * d <= k:
        if k % d == 0:
            break
        d += 1
    else:
        count += 1
print(count)
