-- CreateTable
-- id is the GitHub-supplied installation id; not auto-incremented
CREATE TABLE "GithubInstallation" (
    "id" INTEGER NOT NULL PRIMARY KEY,
    "accountLogin" TEXT NOT NULL,
    "accountType" TEXT NOT NULL,
    "accountId" INTEGER NOT NULL,
    "repositoriesJson" TEXT NOT NULL DEFAULT '[]',
    "createdAt" DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updatedAt" DATETIME NOT NULL
);

-- CreateTable
CREATE TABLE "GithubUserLink" (
    "id" TEXT NOT NULL PRIMARY KEY,
    "userId" TEXT NOT NULL,
    "githubLogin" TEXT NOT NULL,
    "githubId" INTEGER NOT NULL,
    "accessToken" TEXT,
    "createdAt" DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updatedAt" DATETIME NOT NULL
);

-- CreateIndex
CREATE UNIQUE INDEX "GithubUserLink_userId_key" ON "GithubUserLink"("userId");

-- CreateIndex
CREATE UNIQUE INDEX "GithubUserLink_githubId_key" ON "GithubUserLink"("githubId");
