/* Packaging fixture only. NOT the Go product; never deploy this executable. */
#include <stdio.h>
#include <string.h>

int main(int argc, char **argv) {
    if (argc == 2 && strcmp(argv[1], "version") == 0) {
        puts("PACKAGING TEST STUB - not etcd-backup-keeper product acceptance");
        return 0;
    }
    FILE *marker = fopen("/tmp/etcd-backup-keeper-unexpected-execution", "w");
    if (marker != NULL) {
        fputs("Unexpected non-version invocation of packaging stub\n", marker);
        fclose(marker);
    }
    fputs("PACKAGING TEST STUB refuses all non-version operations\n", stderr);
    return 99;
}
